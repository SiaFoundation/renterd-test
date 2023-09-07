package testing

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Mikubill/gofakes3"
	"github.com/google/go-cmp/cmp"
	"github.com/minio/minio-go/v7"
	"go.sia.tech/renterd/api"
	"lukechampine.com/frand"
)

func TestS3(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	cluster, err := newTestCluster(t.TempDir(), newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cluster.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()
	s3 := cluster.S3

	// delete default bucket before testing.
	if err := cluster.Bus.DeleteBucket(context.Background(), api.DefaultBucketName); err != nil {
		t.Fatal(err)
	}

	// add hosts
	if _, err := cluster.AddHostsBlocking(testRedundancySettings.TotalShards); err != nil {
		t.Fatal(err)
	}

	// Create bucket.
	err = s3.MakeBucket(context.Background(), "bucket1", minio.MakeBucketOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// List bucket.
	buckets, err := s3.ListBuckets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 {
		t.Fatal("expected 1 bucket")
	}
	if buckets[0].Name != "bucket1" {
		t.Fatal("expected bucket1", buckets[0].Name)
	} else if buckets[0].CreationDate.IsZero() {
		t.Fatal("expected non-zero creation date")
	}

	// Exists bucket.
	exists, err := s3.BucketExists(context.Background(), "bucket1")
	if err != nil {
		t.Fatal(err)
	} else if !exists {
		t.Fatal("expected bucket1 to exist")
	}
	exists, err = s3.BucketExists(context.Background(), "bucket2")
	if err != nil {
		t.Fatal(err)
	} else if exists {
		t.Fatal("expected bucket2 to not exist")
	}

	// PutOBject into bucket.
	data := frand.Bytes(10)
	_, err = s3.PutObject(context.Background(), "bucket1", "object1", bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s3.PutObject(context.Background(), "bucket2", "object2", bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{})
	if err == nil || !strings.Contains(err.Error(), "The specified bucket does not exist") {
		t.Fatal(err)
	}

	// Get object.
	obj, err := s3.GetObject(context.Background(), "bucket1", "object1", minio.GetObjectOptions{})
	if err != nil {
		t.Fatal(err)
	} else if b, err := io.ReadAll(obj); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(b, data) {
		t.Fatal("data mismatch")
	}

	// Try to delete full bucket.
	err = s3.RemoveBucket(context.Background(), "bucket1")
	if err == nil || !strings.Contains(err.Error(), gofakes3.ErrBucketNotEmpty.Error()) {
		t.Fatal(err)
	}

	// Remove object.
	err = s3.RemoveObject(context.Background(), "bucket1", "object1", minio.RemoveObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Try to get object.
	_, err = s3.GetObject(context.Background(), "bucket1", "object1", minio.GetObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Delete bucket.
	err = s3.RemoveBucket(context.Background(), "bucket1")
	if err != nil {
		t.Fatal(err)
	}
	exists, err = s3.BucketExists(context.Background(), "bucket1")
	if err != nil {
		t.Fatal(err)
	} else if exists {
		t.Fatal("expected bucket1 to exist")
	}
	err = s3.RemoveBucket(context.Background(), "bucket2")
	if err == nil || !strings.Contains(err.Error(), "The specified bucket does not exist") {
		t.Fatal(err)
	}
}

func TestS3List(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	cluster, err := newTestCluster(t.TempDir(), newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cluster.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}()
	s3 := cluster.S3

	// Enable upload packing to speed up test.
	err = cluster.Bus.UpdateSetting(context.Background(), api.SettingUploadPacking, api.UploadPackingSettings{
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// add hosts
	if _, err := cluster.AddHostsBlocking(testRedundancySettings.TotalShards); err != nil {
		t.Fatal(err)
	}

	// Create bucket.
	err = s3.MakeBucket(context.Background(), "bucket", minio.MakeBucketOptions{})
	if err != nil {
		t.Fatal(err)
	}

	objects := []string{
		"/foo/bar",
		"/foo/bat",
		"/foo/baz/quux",
		"/foo/baz/quuz",
		"/gab/guub",
		"/fileś/śpecial",
		"/FOO/bar",
	}
	for _, object := range objects {
		data := frand.Bytes(10)
		_, err = s3.PutObject(context.Background(), "bucket", object, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}

	flatten := func(res <-chan minio.ObjectInfo) []string {
		var objs []string
		for obj := range res {
			if obj.Err != nil {
				t.Fatal(err)
			}
			objs = append(objs, obj.Key)
		}
		return objs
	}

	// {"/", "", []api.ObjectMetadata{{Name: "/FOO/", Size: 7, Health: 1}, {Name: "/fileś/", Size: 6, Health: 1}, {Name: "/foo/", Size: 10, Health: 1}, {Name: "/gab/", Size: 5, Health: 1}}},
	// 	{"/foo/", "", []api.ObjectMetadata{{Name: "/foo/bar", Size: 1, Health: 1}, {Name: "/foo/bat", Size: 2, Health: 1}, {Name: "/foo/baz/", Size: 7, Health: 1}}},
	// 	{"/foo/baz/", "", []api.ObjectMetadata{{Name: "/foo/baz/quux", Size: 3, Health: 1}, {Name: "/foo/baz/quuz", Size: 4, Health: 1}}},
	// 	{"/gab/", "", []api.ObjectMetadata{{Name: "/gab/guub", Size: 5, Health: 1}}},
	// 	{"/fileś/", "", []api.ObjectMetadata{{Name: "/fileś/śpecial", Size: 6, Health: 1}}},

	// 	{"/", "f", []api.ObjectMetadata{{Name: "/fileś/", Size: 6, Health: 1}, {Name: "/foo/", Size: 10, Health: 1}}},
	// 	{"/", "F", []api.ObjectMetadata{{Name: "/FOO/", Size: 7, Health: 1}}},
	// 	{"/foo/", "fo", []api.ObjectMetadata{}},
	// 	{"/foo/baz/", "quux", []api.ObjectMetadata{{Name: "/foo/baz/quux", Size: 3, Health: 1}}},
	// 	{"/gab/", "/guub", []api.ObjectMetadata{}},
	tests := []struct {
		prefix string
		want   []string
	}{
		{
			prefix: "",
			want:   []string{"/FOO/", "/fileś/", "/foo/", "/gab/"},
		},
		{
			prefix: "/foo/",
			want:   []string{"/foo/bar", "/foo/bat", "/foo/baz/"},
		},
		{
			prefix: "/F",
			want:   []string{"/FOO/"},
		},
	}
	for i, test := range tests {
		got := flatten(s3.ListObjects(context.Background(), "bucket", minio.ListObjectsOptions{Prefix: test.prefix}))
		if !cmp.Equal(test.want, got) {
			t.Errorf("test %d: unexpected response: %v", i, cmp.Diff(test.want, got))
		}

		for offset := 0; offset < len(test.want); offset++ {
			got := flatten(s3.ListObjects(context.Background(), "bucket", minio.ListObjectsOptions{Prefix: test.prefix, StartAfter: test.want[offset]}))
			var want []string
			if offset+1 < len(test.want) {
				want = test.want[offset+1:]
			}
			if !cmp.Equal(want, got) {
				t.Errorf("test %d: unexpected response: got %+v want %+v", i, got, want)
			}
		}
	}
}
