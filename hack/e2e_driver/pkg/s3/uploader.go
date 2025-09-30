package s3

import (
	"bytes"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

// Uploader handles uploading logs to S3
type Uploader struct {
	uploader *s3manager.Uploader
	bucket   string
}

// NewUploader creates a new S3 uploader
func NewUploader(region, bucket string) (*Uploader, error) {
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(region),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %w", err)
	}

	return &Uploader{
		uploader: s3manager.NewUploader(sess),
		bucket:   bucket,
	}, nil
}

// UploadLogFile uploads a log file to S3
func (u *Uploader) UploadLogFile(filePath, objectKey string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	// Upload the file to S3
	_, err = u.uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(u.bucket),
		Key:    aws.String(objectKey),
		Body:   file,
	})
	if err != nil {
		return fmt.Errorf("failed to upload file to S3: %w", err)
	}

	return nil
}

// UploadLogData uploads log data directly to S3
func (u *Uploader) UploadLogData(logData []byte, objectKey string) error {
	// Create a bytes.Reader from the log data
	reader := bytes.NewReader(logData)

	// Upload the data to S3
	_, err := u.uploader.Upload(&s3manager.UploadInput{
		Bucket:      aws.String(u.bucket),
		Key:         aws.String(objectKey),
		Body:        reader,
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("failed to upload data to S3: %w", err)
	}

	return nil
}
