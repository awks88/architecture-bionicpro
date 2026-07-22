package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type objectStore struct {
	client     *minio.Client
	bucket     string
	cdnBaseURL string
	keySecret  []byte
}

func newObjectStore(cfg config) (*objectStore, error) {
	client, err := minio.New(cfg.S3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure: cfg.S3Secure,
	})
	if err != nil {
		return nil, fmt.Errorf("create S3 client: %w", err)
	}
	return &objectStore{
		client:     client,
		bucket:     cfg.S3Bucket,
		cdnBaseURL: cfg.CDNBaseURL,
		keySecret:  []byte(cfg.ObjectKeySecret),
	}, nil
}

func (s *objectStore) ready(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("bucket %s does not exist", s.bucket)
	}
	return nil
}

func (s *objectStore) exists(ctx context.Context, objectKey string) (bool, error) {
	_, err := s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	response := minio.ToErrorResponse(err)
	if response.StatusCode == 404 || response.Code == "NoSuchKey" || response.Code == "NoSuchObject" {
		return false, nil
	}
	return false, err
}

func (s *objectStore) save(ctx context.Context, objectKey string, report []byte, from, to time.Time) error {
	filename := fmt.Sprintf("bionicpro-report-%s-%s.json", from.Format(time.DateOnly), to.Format(time.DateOnly))
	_, err := s.client.PutObject(
		ctx,
		s.bucket,
		objectKey,
		bytes.NewReader(report),
		int64(len(report)),
		minio.PutObjectOptions{
			ContentType:        "application/json",
			ContentDisposition: fmt.Sprintf(`attachment; filename="%s"`, filename),
			CacheControl:       "public, max-age=86400, immutable",
		},
	)
	return err
}

func (s *objectStore) reportKey(username string, from, to, etlUpdatedAt time.Time) string {
	mac := hmac.New(sha256.New, s.keySecret)
	_, _ = mac.Write([]byte(username))
	userKey := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf(
		"users/%s/%s_%s/etl-%s.json",
		userKey,
		from.Format(time.DateOnly),
		to.Format(time.DateOnly),
		etlUpdatedAt.UTC().Format("20060102T150405Z"),
	)
}

func (s *objectStore) downloadURL(objectKey string) string {
	return fmt.Sprintf("%s/reports/%s", s.cdnBaseURL, objectKey)
}
