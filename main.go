package main

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"context" //required by mongo driver

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"  // required for the prometheus handle
	"golang.org/x/time/rate"
	// mongo driver
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var client *mongo.Client
var coll *mongo.Collection
var ctx context.Context
var sleep = time.Sleep
var logFatal = log.Fatal
var logPrintf = log.Printf
var httpListenAndServe = http.ListenAndServe
var serviceName = "go-demo"
var limiter = rate.NewLimiter(5, 10)
var limitReachedTime = time.Now().Add(time.Second * (-60))
var limitReached = false

type Person struct {
	Name string
}

var (
	histogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Subsystem: "http_server",
		Name:      "resp_time",
		Help:      "Request response time",
	}, []string{
		"service",
		"code",
		"method",
		"path",
	})
)

func main() {
	logPrintf("Starting the application\n")
	if len(os.Getenv("SERVICE_NAME")) > 0 {
		serviceName = os.Getenv("SERVICE_NAME")
	}
	setupDb()
	RunServer()

	defer client.Disconnect(ctx)
}

func init() {
	prometheus.MustRegister(histogram)
}

// TODO: Test

func setupDb() {
	var err error
	envVar := "DB"
	if len(os.Getenv("DB_ENV")) > 0 {
		envVar = os.Getenv("DB_ENV")
	}
	db := os.Getenv(envVar)
	if len(db) == 0 {
		db = "localhost"
	}
	// DB URI starts with mongodb://
	logPrintf("Connecting DB %s\n", db)
	client, err := mongo.NewClient(options.Client().ApplyURI(db))
	if err != nil {
		panic(err)
	}
	ctx, _ = context.WithTimeout(context.Background(), 10*time.Second)
	err = client.Connect(ctx)
	if err != nil {
		log.Fatal(err)
	}
	err = client.Ping(ctx, readpref.Primary())
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Connected ...")
	coll = client.Database("test").Collection("people")
}

func RunServer() {
	logPrintf("Running the server\n")
	mux := http.NewServeMux()
	mux.HandleFunc("/demo/hello", HelloServer)
	mux.HandleFunc("/version", VersionServer)
	mux.HandleFunc("/demo/person", PersonServer)
	mux.HandleFunc("/demo/random-error", RandomErrorServer)
	mux.HandleFunc("/limiter", LimiterServer)
	mux.Handle("/metrics", prometheusHandler())
	mux.HandleFunc("/", VersionServer)
	logFatal("ListenAndServe: ", httpListenAndServe(":8080", mux))
}

func HelloServer(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	defer func() { recordMetrics(start, req, http.StatusOK) }()

	logPrintf("%s request to %s\n", req.Method, req.RequestURI)
	delay := req.URL.Query().Get("delay")
	msg := "hello, k8s"
	version := os.Getenv("VERSION")
	if len(version) > 0 {
		msg = fmt.Sprintf("%s with version %s", msg, version)
	}
	msg = fmt.Sprintf("%s!\n", msg)
	if len(delay) > 0 {
		delayNum, _ := strconv.Atoi(delay)
		sleep(time.Duration(delayNum) * time.Millisecond)
	}
	io.WriteString(w, msg)
}

func VersionServer(w http.ResponseWriter, req *http.Request) {
	logPrintf("%s request to %s\n", req.Method, req.RequestURI)
	release := req.Header.Get("release")
	if release == "" {
		release = "unknown"
	}
	msg := fmt.Sprintf("Version: %s; Release: %s\n", os.Getenv("VERSION"), release)
	io.WriteString(w, msg)
}

func LimiterServer(w http.ResponseWriter, req *http.Request) {
	logPrintf("%s request to %s\n", req.Method, req.RequestURI)
	if limiter.Allow() == false {
		logPrintf("Limiter in action")
		http.Error(w, http.StatusText(500), http.StatusTooManyRequests)
		limitReached = true
		limitReachedTime = time.Now()
		return
	} else if time.Since(limitReachedTime).Seconds() < 15 {
		logPrintf("Cooling down after the limiter")
		http.Error(w, http.StatusText(500), http.StatusTooManyRequests)
		return
	}
	msg := fmt.Sprintf("Everything is OK\n")
	io.WriteString(w, msg)
}

func RandomErrorServer(w http.ResponseWriter, req *http.Request) {
	code := http.StatusOK
	start := time.Now()
	defer func() { recordMetrics(start, req, code) }()

	logPrintf("%s request to %s\n", req.Method, req.RequestURI)
	rand.Seed(time.Now().UnixNano())
	n := rand.Intn(5)
	msg := "Everything is still OK"
	version := os.Getenv("VERSION")
	if len(version) > 0 {
		msg = fmt.Sprintf("%s with version %s", msg, version)
	}
	msg = fmt.Sprintf("%s\n", msg)
	if n == 0 {
		code = http.StatusInternalServerError
		msg = "ERROR: Something, somewhere, went wrong!\n"
		logPrintf(msg)
	}
	w.WriteHeader(code)
	io.WriteString(w, msg)
}

func PersonServer(w http.ResponseWriter, req *http.Request) {
	code := http.StatusOK
	start := time.Now()
	defer func() { recordMetrics(start, req, code) }()

	logPrintf("%s request to %s\n", req.Method, req.RequestURI)
	msg := "Everything is OK"
	if req.Method == "PUT" {
		name := req.URL.Query().Get("name")
		if _, err := upsertName(name); err != nil {
			code = http.StatusInternalServerError
			msg = err.Error()
		}
	} else {
		var res []Person
		cur, err := findPeople()
		if err != nil {
			panic(err)
		}
		if err = cur.All(ctx, &res); err != nil {
			panic(err)
		}
		var names []string
		for _, p := range res {
			names = append(names, p.Name)
		}
		msg = strings.Join(names, "\n")
	}
	w.WriteHeader(code)
	io.WriteString(w, msg)
}

var prometheusHandler = func() http.Handler {
	return promhttp.Handler() // new version
}

var findPeople = func() (cur *mongo.Cursor, err error){
	setupDb()
	return coll.Find(ctx, bson.D{})
}

var upsertName = func(name string) (info *mongo.UpdateResult, err error) {
	setupDb()
	filter:= bson.D{{"name", name}}
	update := bson.D{{"$set", bson.D{{"name", name}}}}
	opts := options.Update().SetUpsert(true)
        return coll.UpdateOne(ctx, filter, update, opts)
}

func recordMetrics(start time.Time, req *http.Request, code int) {
	duration := time.Since(start)
	histogram.With(
		prometheus.Labels{
			"service": serviceName,
			"code":    fmt.Sprintf("%d", code),
			"method":  req.Method,
			"path":    req.URL.Path,
		},
	).Observe(duration.Seconds())
}
