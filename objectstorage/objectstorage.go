package objectstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Anti-Raid/jobserver/config"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// A simple abstraction for object storage
type ObjectStorage struct {
	c *config.ObjectStorageConfig

	// If s3-like
	minio *minio.Client

	// if s3-like
	cdnMinio *minio.Client
}

func New(c *config.ObjectStorageConfig) (o *ObjectStorage, err error) {
	o = &ObjectStorage{
		c: c,
	}

	switch c.Type {
	case "s3-like":
		o.minio, err = minio.New(c.Endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""),
			Secure: c.Secure,
		})

		if err != nil {
			return nil, err
		}

		if strings.HasPrefix(c.CdnEndpoint, "$DOCKER:") {
			// Docker's a bit *special*
			o.cdnMinio, err = minio.New(c.Endpoint, &minio.Options{
				Creds:  credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""),
				Secure: c.Secure,
			})

			if err != nil {
				return nil, err
			}
		} else {
			o.cdnMinio, err = minio.New(c.CdnEndpoint, &minio.Options{
				Creds:  credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""),
				Secure: c.CdnSecure,
			})

			if err != nil {
				return nil, err
			}
		}
	case "local":
		err = os.MkdirAll(c.BasePath, 0755)

		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("invalid object storage type")
	}

	return o, nil
}

func (o *ObjectStorage) ensureBucketExists(ctx context.Context, bucketName string) error {
	switch o.c.Type {
	case "s3-like":
		exists, err := o.minio.BucketExists(ctx, o.c.BasePath+bucketName)

		if err != nil {
			return err
		}

		if exists {
			return nil
		}

		return o.minio.MakeBucket(ctx, o.c.BasePath+bucketName, minio.MakeBucketOptions{})
	}

	return nil
}

// Saves a file to the object storage
//
// Note that 'expiry' is not supported for local storage
func (o *ObjectStorage) Save(ctx context.Context, bucketName, dir, filename string, data *bytes.Buffer, expiry time.Duration) error {
	if err := o.ensureBucketExists(ctx, bucketName); err != nil {
		return err
	}

	if filename == "" {
		return fmt.Errorf("filename cannot be empty")
	}

	switch o.c.Type {
	case "local":
		err := os.MkdirAll(filepath.Join(o.c.BasePath, bucketName, dir), 0755)

		if err != nil {
			return err
		}

		f, err := os.Create(filepath.Join(o.c.BasePath, bucketName, dir, filename))

		if err != nil {
			return err
		}

		_, err = io.Copy(f, data)

		if err != nil {
			return err
		}

		return nil
	case "s3-like":
		p := minio.PutObjectOptions{}

		if expiry != 0 {
			p.Expires = time.Now().Add(expiry)
		}
		_, err := o.minio.PutObject(ctx, o.c.BasePath+bucketName, dir+"/"+filename, data, int64(data.Len()), p)

		if err != nil {
			return err
		}

		return nil
	default:
		return fmt.Errorf("operation not supported for object storage type %s", o.c.Type)
	}
}

// Returns the url to the file
func (o *ObjectStorage) GetUrl(ctx context.Context, bucketName, dir, filename string, urlExpiry time.Duration, internal bool) (*url.URL, error) {
	if err := o.ensureBucketExists(ctx, bucketName); err != nil {
		return nil, err
	}

	switch o.c.Type {
	case "local":
		var path string

		if filename == "" {
			path = filepath.Join(o.c.BasePath, bucketName, dir)
		} else {
			path = filepath.Join(o.c.BasePath, bucketName, dir, filename)
		}

		return &url.URL{
			Scheme: "file",
			Path:   path,
		}, nil
	case "s3-like":
		var path string

		if filename == "" {
			path = dir
		} else {
			path = dir + "/" + filename
		}

		var p *url.URL
		var err error
		if internal {
			p, err = o.minio.PresignedGetObject(ctx, o.c.BasePath+bucketName, path, urlExpiry, nil)

			if err != nil {
				return nil, err
			}
		} else {
			p, err = o.cdnMinio.PresignedGetObject(ctx, o.c.BasePath+bucketName, path, urlExpiry, nil)

			if err != nil {
				return nil, err
			}

			// One more patch is needed for docker to swap out the endpoint for CDN endpoint
			//
			// The NGINX proxy layer will then swap the CDN endpoint for endpoint again in its X-Forwarded headers
			if strings.HasPrefix(o.c.CdnEndpoint, "$DOCKER:") {
				p.Scheme = "http"
				p.Host = strings.TrimPrefix(o.c.CdnEndpoint, "$DOCKER:")
			}
		}

		return p, nil
	default:
		return nil, fmt.Errorf("operation not supported for object storage type %s", o.c.Type)
	}
}

// Deletes a file
func (o *ObjectStorage) Delete(ctx context.Context, bucketName, dir, filename string) error {
	if err := o.ensureBucketExists(ctx, bucketName); err != nil {
		return err
	}

	switch o.c.Type {
	case "local":
		if filename == "" {
			return os.RemoveAll(filepath.Join(o.c.BasePath, bucketName, dir))
		}

		return os.Remove(filepath.Join(o.c.BasePath, bucketName, dir, filename))
	case "s3-like":
		if filename == "" {
			return o.minio.RemoveObject(ctx, o.c.BasePath+bucketName, dir, minio.RemoveObjectOptions{})
		}

		return o.minio.RemoveObject(ctx, o.c.BasePath+bucketName, dir+"/"+filename, minio.RemoveObjectOptions{})
	default:
		return fmt.Errorf("operation not supported for object storage type %s", o.c.Type)
	}
}

// Returns the name of the bucket for the given guild
func GuildBucket(guildId string) string {
	return "antiraid.guild." + guildId
}
