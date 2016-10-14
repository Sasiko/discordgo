package ratelimit

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RateLimiter struct {
	sync.Mutex
	buckets         map[string]*Bucket
	globalRateLimit time.Duration
}

func New() *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*Bucket),
	}
}

func (r *RateLimiter) getBucket(key string) *Bucket {
	if bucket, ok := r.buckets[key]; ok {
		return bucket
	}

	b := &Bucket{remaining: 1, r: r, Key: key}
	r.buckets[key] = b
	return b
}

// Locks untill were allowed to make a request
func (r *RateLimiter) LockBucket(path string) *Bucket {
	bucketKey := ParseURL(path)
	log.Println("Before:", path, "after:", bucketKey)

	r.Lock()
	b := r.getBucket(bucketKey)
	r.Unlock()

	b.mu.Lock()

	// If we ran out of calls and the reset time is still ahead of us
	// then we need to take it easy and relax a little
	for b.remaining < 1 && b.reset.After(time.Now()) {
		// Sleep for an extra 500ms incase of time slighly out of sync
		// (i got ratelimited for 1 and 2 milliseconds a lot when testing...)
		toSleep := b.reset.Sub(time.Now()) + time.Millisecond*500
		time.Sleep(toSleep)

	}

	// Lock and unlock to check for global ratelimites after sleeping
	r.Lock()
	r.Unlock()

	b.remaining--
	log.Println(b.remaining)
	return b
}

type Bucket struct {
	Key string

	mu        sync.Mutex
	remaining int
	limit     int
	reset     time.Time
	r         *RateLimiter
}

// Release unlocks the bucket and reads the headers to update the bucket's ratelimit info
// to the relevant bucket or locks up the whole thing in case of a global
// ratelimit.
func (b *Bucket) Release(headers http.Header) error {
	defer b.mu.Unlock()
	if headers == nil {
		log.Println("Null headers")
		return nil
	}

	remaining := headers.Get("X-RateLimit-Remaining")
	reset := headers.Get("X-RateLimit-Reset")
	global := headers.Get("X-RateLimit-Global")

	// If it's global just keep the main ratelimit mutex locked
	if global != "" {
		retryAfer, err := strconv.Atoi(headers.Get("Retry-After"))
		if err != nil {
			return err
		}

		go func() {
			b.r.Lock()
			time.Sleep(time.Millisecond * time.Duration(retryAfer))
			b.r.Unlock()
		}()

		log.Println("GLOBAL RATELIMIT", global)
		return nil
	}

	if reset == "" || remaining == "" {
		log.Println("RESET OR REMAINING EMPTY")
		return errors.New("No ratelimit headers provided")
	}

	parsedReset, err := strconv.ParseInt(reset, 10, 64)
	if err != nil {
		return err
	}

	parsedRemaining, err := strconv.ParseInt(remaining, 10, 32)
	if err != nil {
		return err
	}

	b.remaining = int(parsedRemaining)
	b.reset = time.Unix(parsedReset, 0)

	log.Println(b.Key, parsedReset)
	return nil
}

var (
	urlVarRegex = regexp.MustCompile(`[a-z]+\/[0-9]+`)

	majoyVariables = []string{
		"channels",
		"guilds",
	}
)

// Parses the url, removing everything not relevant to identifying a bucket.
// such as minor variables
func ParseURL(url string) string {

	// Remove url parameters
	noParam := strings.SplitN(url, "?", 2)[0]

	// Remove minor url variables
	result := urlVarRegex.ReplaceAllStringFunc(noParam, func(s string) string {
		split := strings.SplitN(s, "/", 2)

		for _, major := range majoyVariables {
			if split[0] == major {
				// It's a major variable
				return s
			}
		}

		// It's a minor variable, strip the value
		return split[0] + "/"
	})
	return result

}
