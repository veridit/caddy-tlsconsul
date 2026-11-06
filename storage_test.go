package storageconsul

import (
	"context"
	"io/fs"
	"os"
	"path"
	"sync"
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
// acquired for the same key. A test hook parks the stale watcher of the
// first lock until the key has been re-locked, then lets it run, so the
// interleaving that triggers the bug happens on every run. The check is
// server-side: Consul must still show the key as held and a second,
// independent storage instance must be unable to acquire it.
func TestConsulStorage_RelockSurvivesStaleWatcher(t *testing.T) {
	cs := setupConsulEnv(t)
	other := setupConsulEnv(t)
	// Consul's blocking lock query only observes context cancellation
	// between waits, so keep the wait short for the expected-to-fail Lock.
	other.Timeout = 1
	lockKey := path.Join("acme", "relock.example.com", "lock")

	parked := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	hook := func(key string) func() {
		first := false
		if key == lockKey {
			once.Do(func() { first = true })
		}
		if !first {
			return func() {}
		}
		close(parked)
		<-release
		return func() { close(finished) }
	}
	testHookLostLock.Store(&hook)
	t.Cleanup(func() { testHookLostLock.Store(nil) })

	if !assert.NoError(t, cs.Lock(context.Background(), lockKey)) {
		return
	}
	assert.NoError(t, cs.Unlock(context.Background(), lockKey))
	select {
	case <-parked:
	case <-time.After(10 * time.Second):
		t.Fatal("watcher of the first lock did not observe the lost lock")
	}
	if !assert.NoError(t, cs.Lock(context.Background(), lockKey)) {
		close(release)
		return
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("stale watcher did not finish")
	}

	kv, _, err := cs.ConsulClient.KV().Get(cs.prefixKey(lockKey), nil)
	assert.NoError(t, err)
	assert.True(t, kv != nil && kv.Session != "", "Consul shows no lock holder after the stale watcher ran")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	err = other.Lock(ctx, lockKey)
	cancel()
	if !assert.Error(t, err, "second instance acquired a lock that should still be held") {
		_ = other.Unlock(context.Background(), lockKey)
	}

	_ = cs.Unlock(context.Background(), lockKey)
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

// Two goroutines of one process (e.g. two certmagic Configs during a
// reload) must not both hold the same key: the second Lock has to wait
// for the first Unlock instead of returning nil immediately.
func TestConsulStorage_SameProcessMutualExclusion(t *testing.T) {
	cs := setupConsulEnv(t)
	lockKey := path.Join("acme", "inprocess.example.com", "lock")

	assert.NoError(t, cs.Lock(context.Background(), lockKey))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	err := cs.Lock(ctx, lockKey)
	cancel()
	assert.Error(t, err, "second in-process Lock must not succeed while the key is held")

	acquired := make(chan error, 1)
	go func() { acquired <- cs.Lock(context.Background(), lockKey) }()
	select {
	case err := <-acquired:
		t.Fatalf("second Lock returned before Unlock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	assert.NoError(t, cs.Unlock(context.Background(), lockKey))
	select {
	case err := <-acquired:
		assert.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("second Lock did not acquire after Unlock")
	}
	assert.NoError(t, cs.Unlock(context.Background(), lockKey))
}
