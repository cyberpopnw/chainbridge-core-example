package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/ChainSafe/chainbridge-core/flags"
	"github.com/go-redis/redis/v8"
	"github.com/spf13/viper"
)

var db *redis.Client

func hello(w http.ResponseWriter, req *http.Request) {
	ctx := context.Background()
	var (
		index uint64
		resp  []string
	)
	for {
		keys, index, err := db.Scan(ctx, index, "*", 10).Result()
		if err != nil || index == 0 {
			break
		}
		for _, key := range keys {
			if value, err := db.Get(ctx, key).Result(); err != nil {
				resp = append(resp, value)
			}
		}
	}
	fmt.Fprintf(w, strings.Join(resp, ""))
}

func headers(w http.ResponseWriter, req *http.Request) {

	for name, headers := range req.Header {
		for _, h := range headers {
			fmt.Fprintf(w, "%v: %v\n", name, h)
		}
	}
}

func Run() error {
	var err error
	opts, err := redis.ParseURL(viper.GetString(flags.MessageStoreFlagName))
	if err != nil {
		return err
	}
	db = redis.NewClient(opts)
	defer db.Close()
	http.HandleFunc("/hello", hello)
	http.HandleFunc("/headers", headers)

	return http.ListenAndServe(":8090", nil)
}
