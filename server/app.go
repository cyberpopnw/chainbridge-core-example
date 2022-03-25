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

func deposits(w http.ResponseWriter, req *http.Request) {
	setupResponse(&w, req)
	if (*req).Method == "OPTIONS" {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	ctx := context.Background()
	var (
		index uint64
		resp  []string
	)
	for {
		keys, index, err := db.Scan(ctx, index, "*", 10).Result()
		if err != nil {
			break
		}
		for _, key := range keys {
			if value, err := db.Get(ctx, key).Result(); err == nil {
				resp = append(resp, value)
			}
		}
		// No more elements
		if index == 0 {
			break
		}
	}
	fmt.Fprint(w, "[", strings.Join(resp, ","), "]")
}

func setupResponse(w *http.ResponseWriter, req *http.Request) {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
    (*w).Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
    (*w).Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
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
	http.HandleFunc("/deposits", deposits)
	http.HandleFunc("/headers", headers)

	return http.ListenAndServe(":8090", nil)
}
