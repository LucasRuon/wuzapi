package main

import (
	"context"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"go.mau.fi/whatsmeow"
)

const (
	downloadTimeoutImage    = 2 * time.Minute
	downloadTimeoutAudio    = 5 * time.Minute
	downloadTimeoutDocument = 10 * time.Minute
	downloadTimeoutVideo    = 10 * time.Minute
	downloadTimeoutSticker  = 1 * time.Minute
)

type mediaS3Config struct {
	Enabled       string
	MediaDelivery string
}

// Limite (em bytes) acima do qual a midia NAO e embutida em base64 no webhook.
// Base64 de midia grande custa ~10x o tamanho do arquivo em memoria (copias do
// download, do JSON e do body de cada webhook) e os receptores devolvem 413
// (n8n limita a 16 MB por padrao). Configuravel por WUZAPI_MAX_BASE64_MEDIA_MB;
// 0 desativa o limite.
const defaultMaxBase64MediaMB = 10

var maxBase64MediaBytes = loadMaxBase64MediaBytes()

func loadMaxBase64MediaBytes() int64 {
	mb := int64(defaultMaxBase64MediaMB)
	if v := os.Getenv("WUZAPI_MAX_BASE64_MEDIA_MB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			mb = n
		} else {
			log.Warn().Str("value", v).Msg("WUZAPI_MAX_BASE64_MEDIA_MB invalido, usando padrao")
		}
	}
	return mb * 1024 * 1024
}

// fileLengther e implementado por todos os *Message de midia do waE2E
// (Image/Video/Audio/Document/Sticker); permite conhecer o tamanho antes de baixar.
type fileLengther interface {
	GetFileLength() uint64
}

func exceedsBase64Limit(size int64) bool {
	return maxBase64MediaBytes > 0 && size > maxBase64MediaBytes
}

func (mycli *MyClient) processMedia(
	msg whatsmeow.DownloadableMessage,
	mimeType string,
	fallbackExt string,
	timeout time.Duration,
	isIncoming bool,
	chatJID string,
	messageID string,
	s3cfg mediaS3Config,
	postmap map[string]interface{},
	extraKeys map[string]interface{},
) {
	wantS3 := s3cfg.Enabled == "true" && (s3cfg.MediaDelivery == "s3" || s3cfg.MediaDelivery == "both")
	wantBase64 := s3cfg.MediaDelivery == "base64" || s3cfg.MediaDelivery == "both"

	// Tamanho declarado pelo remetente; permite pular o download quando a unica
	// entrega seria base64 e a midia estoura o limite.
	var declaredSize int64
	if fl, ok := msg.(fileLengther); ok {
		declaredSize = int64(fl.GetFileLength())
	}
	if wantBase64 && exceedsBase64Limit(declaredSize) {
		postmap["mimeType"] = mimeType
		postmap["fileSize"] = declaredSize
		postmap["base64Skipped"] = true
		postmap["base64SkipReason"] = "media exceeds WUZAPI_MAX_BASE64_MEDIA_MB"
		log.Warn().Str("messageID", messageID).Int64("size", declaredSize).Int64("limit", maxBase64MediaBytes).Msg("Media too large for base64 delivery; skipping base64")
		wantBase64 = false
		if !wantS3 {
			return
		}
	}

	tmpDir := filepath.Join("/tmp", "user_"+mycli.userID)
	if err := os.MkdirAll(tmpDir, 0751); err != nil {
		log.Error().Err(err).Msg("Could not create temporary directory")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	data, err := mycli.WAClient.Download(ctx, msg)
	if err != nil {
		log.Error().Err(err).Msg("Failed to download media")
		return
	}

	// Tamanho real (o declarado pode vir zerado ou errado).
	if wantBase64 && exceedsBase64Limit(int64(len(data))) {
		postmap["mimeType"] = mimeType
		postmap["fileSize"] = len(data)
		postmap["base64Skipped"] = true
		postmap["base64SkipReason"] = "media exceeds WUZAPI_MAX_BASE64_MEDIA_MB"
		log.Warn().Str("messageID", messageID).Int("size", len(data)).Int64("limit", maxBase64MediaBytes).Msg("Media too large for base64 delivery; skipping base64")
		wantBase64 = false
		if !wantS3 {
			return
		}
	}

	ext := fallbackExt
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		ext = exts[0]
	}
	tmpPath := filepath.Join(tmpDir, messageID+ext)

	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		log.Error().Err(err).Msg("Failed to save media to temporary file")
		return
	}
	defer func() {
		if err := os.Remove(tmpPath); err != nil {
			log.Error().Err(err).Msg("Failed to delete temporary file")
		} else {
			log.Info().Str("path", tmpPath).Msg("Temporary file deleted")
		}
	}()

	if wantS3 {
		s3Data, err := GetS3Manager().ProcessMediaForS3(
			ctx,
			mycli.userID,
			chatJID,
			messageID,
			data,
			mimeType,
			filepath.Base(tmpPath),
			isIncoming,
		)
		if err != nil {
			log.Error().Err(err).Msg("Failed to upload media to S3")
		} else {
			postmap["s3"] = s3Data
		}
	}

	if wantBase64 {
		b64, mime_, err := fileToBase64(tmpPath)
		if err != nil {
			log.Error().Err(err).Msg("Failed to convert media to base64")
			return
		}
		postmap["base64"] = b64
		postmap["mimeType"] = mime_
		postmap["fileName"] = filepath.Base(tmpPath)
	}

	for k, v := range extraKeys {
		postmap[k] = v
	}

	log.Info().Str("path", tmpPath).Msg("Media processed")
}
