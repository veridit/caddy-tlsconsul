package storageconsul

import (
	"context"
	"io/fs"
	"os"
	"path"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	consul "github.com/hashicorp/consul/api"
	"github.com/stretchr/testify/assert"

	consulContainer "github.com/testcontainers/testcontainers-go/modules/consul"
)

var consulCont *consulContainer.ConsulContainer

const TestPrefix = "consultlstest"

func TestMain(m *testing.M) {
	cc, err := consulContainer.Run(context.Background(), "hashicorp/consul:1.15")
	if err != nil {
		panic(err)
	}

	consulCont = cc
	defer consulCont.Terminate(context.Background())

	m.Run()
}

// these tests need a running Consul server
func setupConsulEnv(t *testing.T) *ConsulStorage {
	os.Setenv(EnvNamePrefix, TestPrefix)
	consulEndpoint, err := consulCont.ApiEndpoint(context.Background())
	assert.NoError(t, err)

	//os.Setenv(consul.HTTPTokenEnvName, "2f9e03f8-714b-5e4d-65ea-c983d6b172c4")
	os.Setenv(consul.HTTPAddrEnvName, consulEndpoint)

	cs := New()
	ctx, _ := caddy.NewContext(caddy.Context{Context: context.Background()})
	cs.Provision(ctx)

	_, err = cs.ConsulClient.KV().DeleteTree(TestPrefix, nil)
	assert.NoError(t, err)

	return cs
}

func TestConsulStorage_Store(t *testing.T) {
	cs := setupConsulEnv(t)

	err := cs.Store(context.Background(), path.Join("acme", "example.com", "sites", "example.com", "example.com.crt"), []byte("crt data"))
	assert.NoError(t, err)
}

func TestConsulStorage_Exists(t *testing.T) {
	cs := setupConsulEnv(t)

	key := path.Join("acme", "example.com", "sites", "example.com", "example.com.crt")

	err := cs.Store(context.Background(), key, []byte("crt data"))
	assert.NoError(t, err)

	exists := cs.Exists(context.Background(), key)
	assert.True(t, exists)
}

func TestConsulStorage_Load(t *testing.T) {
	cs := setupConsulEnv(t)

	key := path.Join("acme", "example.com", "sites", "example.com", "example.com.crt")
	content := []byte("crt data")

	err := cs.Store(context.Background(), key, content)
	assert.NoError(t, err)

	contentLoded, err := cs.Load(context.Background(), key)
	assert.NoError(t, err)

	assert.Equal(t, content, contentLoded)
}

func TestConsulStorage_Delete(t *testing.T) {
	cs := setupConsulEnv(t)

	key := path.Join("acme", "example.com", "sites", "example.com", "example.com.crt")
	content := []byte("crt data")

	err := cs.Store(context.Background(), key, content)
	assert.NoError(t, err)

	err = cs.Delete(context.Background(), key)
	assert.NoError(t, err)

	exists := cs.Exists(context.Background(), key)
	assert.False(t, exists)

	contentLoaded, err := cs.Load(context.Background(), key)
	assert.Nil(t, contentLoaded)
	assert.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestConsulStorage_Stat(t *testing.T) {
	cs := setupConsulEnv(t)

	key := path.Join("acme", "example.com", "sites", "example.com", "example.com.crt")
	content := []byte("crt data")

	err := cs.Store(context.Background(), key, content)
	assert.NoError(t, err)

	info, err := cs.Stat(context.Background(), key)
	assert.NoError(t, err)

	assert.Equal(t, key, info.Key)
}

func TestConsulStorage_List(t *testing.T) {
	cs := setupConsulEnv(t)

	err := cs.Store(context.Background(), path.Join("acme", "example.com", "sites", "example.com", "example.com.crt"), []byte("crt"))
	assert.NoError(t, err)
	err = cs.Store(context.Background(), path.Join("acme", "example.com", "sites", "example.com", "example.com.key"), []byte("key"))
	assert.NoError(t, err)
	err = cs.Store(context.Background(), path.Join("acme", "example.com", "sites", "example.com", "example.com.json"), []byte("meta"))
	assert.NoError(t, err)

	keys, err := cs.List(context.Background(), path.Join("acme", "example.com", "sites", "example.com"), true)
	assert.NoError(t, err)
	assert.Len(t, keys, 3)
	assert.Contains(t, keys, path.Join("acme", "example.com", "sites", "example.com", "example.com.crt"))
}

func TestConsulStorage_ListNonRecursive(t *testing.T) {
	cs := setupConsulEnv(t)

	err := cs.Store(context.Background(), path.Join("acme", "example.com", "sites", "example.com", "example.com.crt"), []byte("crt"))
	assert.NoError(t, err)
	err = cs.Store(context.Background(), path.Join("acme", "example.com", "sites", "example.com", "example.com.key"), []byte("key"))
	assert.NoError(t, err)
	err = cs.Store(context.Background(), path.Join("acme", "example.com", "sites", "example.com", "example.com.json"), []byte("meta"))
	assert.NoError(t, err)

	keys, err := cs.List(context.Background(), path.Join("acme", "example.com", "sites"), false)
	assert.NoError(t, err)

	assert.Len(t, keys, 1)
	assert.Contains(t, keys, path.Join("acme", "example.com", "sites", "example.com"))
}

func TestConsulStorage_LockUnlock(t *testing.T) {
	cs := setupConsulEnv(t)
	lockKey := path.Join("acme", "example.com", "sites", "example.com", "lock")

	err := cs.Lock(context.Background(), lockKey)
	assert.NoError(t, err)

	err = cs.Unlock(context.Background(), lockKey)
	assert.NoError(t, err)
}

// The watcher goroutine of a released lock must not release a newer lock
// acquired for the same key.
func TestConsulStorage_RelockSurvivesStaleWatcher(t *testing.T) {
	cs := setupConsulEnv(t)
	lockKey := path.Join("acme", "relock.example.com", "lock")

	for i := 0; i < 20; i++ {
		assert.NoError(t, cs.Lock(context.Background(), lockKey))
		assert.NoError(t, cs.Unlock(context.Background(), lockKey))
		assert.NoError(t, cs.Lock(context.Background(), lockKey))
		time.Sleep(50 * time.Millisecond)
		_, held := cs.GetLock(lockKey)
		if !assert.True(t, held, "iteration %d: new lock was released by stale watcher", i) {
			return
		}
		assert.NoError(t, cs.Unlock(context.Background(), lockKey))
	}
}

func TestConsulStorage_TwoLocks(t *testing.T) {
	cs := setupConsulEnv(t)
	cs2 := setupConsulEnv(t)
	lockKey := path.Join("acme", "example.com", "sites", "example.com", "lock")

	err := cs.Lock(context.Background(), lockKey)
	assert.NoError(t, err)

	time.AfterFunc(5*time.Second, func() {
		assert.NoError(t, cs.Unlock(context.Background(), lockKey))
	})

	err = cs2.Lock(context.Background(), lockKey)
	assert.NoError(t, err)

	err = cs2.Unlock(context.Background(), lockKey)
	assert.NoError(t, err)
}
