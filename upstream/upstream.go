package upstream

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	byteSizes "github.com/labstack/gommon/bytes"
	"github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttprouter"
)

const (
	letterBytes   = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	letterIdxBits = 6                    // 6 bits to represent a letter index
	letterIdxMask = 1<<letterIdxBits - 1 // All 1-bits, as many as letterIdxBits
	letterIdxMax  = 63 / letterIdxBits   // # of letter indices fitting in 63 bits

	// src: httpbin.org/xml
	xml = `<?xml version='1.0' encoding='us-ascii'?>
<!--  A SAMPLE set of slides  -->
<slideshow title="Sample Slide Show" date="Date of publication" author="Yours Truly">
  <!-- TITLE SLIDE -->
  <slide type="all">
    <title>Wake up to WonderWidgets!</title>
  </slide>

  <!-- OVERVIEW -->
  <slide type="all">
    <title>Overview</title>
    <item>Why <em>WonderWidgets</em> are great</item>
    <item/>
    <item>Who <em>buys</em> WonderWidgets</item>
  </slide>
</slideshow>`
)

type resource struct {
	Id   int
	Name string
}

var resources map[int]resource

func Serve(addr, name string, maxRequestBodySize int) error {
	logrus.Infof("starting server on %s", addr)

	r := registerRoutes()

	s := &fasthttp.Server{
		Handler:            r.Handler,
		Name:               name,
		MaxRequestBodySize: maxRequestBodySize,
	}
	return s.ListenAndServe(addr)
}

func ServeTLS(addr, certFile, keyFile, name string, maxRequestBodySize int) error {
	logrus.Infof("starting TLS server on %s", addr)

	r := registerRoutes()

	s := &fasthttp.Server{
		Handler:            r.Handler,
		Name:               name,
		MaxRequestBodySize: maxRequestBodySize,
	}

	return s.ListenAndServeTLS(addr, certFile, keyFile)
}

func registerRoutes() *fasthttprouter.Router {
	r := fasthttprouter.New()

	r.GET("/echo", echoResponseHandler)
	r.POST("/echo", echoResponseHandler)
	r.GET("/delay/:delay", fixedDelayResponse)
	r.GET("/json/:type", jsonHandler)

	log.Printf("GET /json")
	log.Printf("\tPATH /valid\treturns a valid json response\n")
	log.Printf("\tPATH /invalid\t returns an invalid json response\n")
	log.Printf("\tHEADER X-Delay: 200ms")
	log.Printf("\t\t-> responds in 200ms")
	log.Printf("\tHEADER X-Delay: 100ms, X-Slowdown: 300ms, X-Slowdown-From: 2006-01-02T15:04:05Z07:00\n")
	log.Printf("\t\t-> responds in 200ms, from %s applies a further 300ms slowdown (time.RFC3339)",
		time.Now().Add(time.Minute).Format(time.RFC3339))

	r.GET("/xml", xmlHandler)
	r.POST("/soap", soapHandler)
	r.GET("/size/:size", sizeHandler)
	r.POST("/size/:size", sizeHandler)
	r.PUT("/size/:size", sizeHandler)
	log.Printf("GET /xml")
	log.Printf("\t-> returns sample XML response")
	log.Printf("GET|PUT|POST /size/:size")
	log.Printf("\tPATH /size/1MB\treturns random payload of requested size")
	log.Printf("\tQuery chunked=true\tstreams response in chunks")

	seedResources()
	r.GET("/resource", resourceIndexHandler)
	r.GET("/resource/:id", resourceShowHandler)
	log.Printf("Get /resource")
	log.Printf("\tQuery limit=10\treturns first N resources")
	log.Printf("Get /resource/:id")
	log.Printf("\tPATH /resourc/1\treturns a single resource or 404")

	return r
}

func seedResources() {
	resources = make(map[int]resource, 100)
	for i := 0; i < 100; i++ {
		resources[i] = resource{Id: i, Name: randStringBytesMaskImprSrc(10, rand.NewSource(time.Now().UnixNano()))}
	}
}

type echoResponse struct {
	Method     string
	RequestURI string
	Header     string
	Headers    map[string][]string
	Body       string
	Host       string
	QueryArgs  string
}

func echoResponseHandler(c *fasthttp.RequestCtx, _ fasthttprouter.Params) {
	res := echoResponse{
		Method:     string(c.Method()),
		RequestURI: string(c.RequestURI()),
		Header:     c.Request.Header.String(),
		Headers:    make(map[string][]string),
		Body:       string(c.PostBody()),
		Host:       string(c.Host()),
		QueryArgs:  c.QueryArgs().String(),
	}
	c.Request.Header.All()(func(key, value []byte) bool {
		res.Headers[string(key)] = append(res.Headers[string(key)], string(value))
		return true
	})

	if err := json.NewEncoder(c).Encode(res); err != nil {
		c.Error(err.Error(), fasthttp.StatusInternalServerError)
	}
}

func resourceIndexHandler(c *fasthttp.RequestCtx, _ fasthttprouter.Params) {
	c.SetContentType("application/json")

	if err := applyDelay(c, nil); err != nil {
		return
	}

	limit := c.QueryArgs().GetUintOrZero("limit")
	if limit == 0 {
		limit = 10
	}
	subset := make(map[int]resource)
	for i := 0; i < min(limit, len(resources)); i++ {
		subset[i] = resources[i]
	}

	jsBytes, _ := json.Marshal(subset)

	fmt.Fprint(c, string(jsBytes))
}

func resourceShowHandler(c *fasthttp.RequestCtx, p fasthttprouter.Params) {
	c.SetContentType("application/json")

	if err := applyDelay(c, nil); err != nil {
		return
	}

	id, err := strconv.Atoi(p.ByName("id"))
	if err != nil {
		return
	}
	res, ok := resources[id]
	if !ok {
		c.SetStatusCode(http.StatusNotFound)
		fmt.Fprint(c, "Not Found")
		return
	}

	jsBytes, _ := json.Marshal(res)

	fmt.Fprint(c, string(jsBytes))
}

func applyDelay(c *fasthttp.RequestCtx, _ fasthttprouter.Params) error {
	delay := string(c.Request.Header.Peek("X-Delay"))
	percentStr := string(c.Request.Header.Peek("X-Delay-Percent"))

	if delay == "" {
		return nil
	}

	duration, err := time.ParseDuration(delay)
	if err != nil {
		return err
	}

	percent := int64(100)
	percent, err = strconv.ParseInt(percentStr, 10, 0)
	if err != nil {
		percent = 100
	}

	r := rand.Int63n(100)

	if percent > r {
		time.Sleep(duration)
	}

	return nil
}

var (
	start = time.Now()
)

func applySlowdown(c *fasthttp.RequestCtx, _ fasthttprouter.Params) error {
	delayHeader := string(c.Request.Header.Peek("X-Slowdown"))
	fromTimeHeader := string(c.Request.Header.Peek("X-Slowdown-From"))

	if delayHeader == "" {
		return nil
	}
	if fromTimeHeader == "" {
		return nil
	}

	from, err := time.Parse(time.RFC3339, fromTimeHeader)
	if err != nil {
		return err
	}

	delay, err := time.ParseDuration(delayHeader)
	if err != nil {
		return err
	}

	if start.After(from) {
		time.Sleep(delay)
	}

	return nil
}

func sizeHandler(c *fasthttp.RequestCtx, p fasthttprouter.Params) {
	// Parse parses human readable bytes string to bytes integer.
	// For example, 6GB (6G is also valid) will return 6442450944.
	size, err := byteSizes.Parse(p.ByName("size"))
	if err != nil {
		return
	}

	if err := applyDelay(c, nil); err != nil {
		return
	}

	src := rand.NewSource(time.Now().UnixNano())
	q := c.QueryArgs()
	chunked := !strings.EqualFold(string(q.Peek("chunked")), "false")
	acceptEncoding := strings.ToLower(string(c.Request.Header.Peek("Accept-Encoding")))
	useGzip := strings.Contains(acceptEncoding, "gzip") && q.GetBool("gzip")

	if useGzip && !chunked {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write([]byte(randStringBytesMaskImprSrc(int(size), src)))
		gz.Close()
		c.Response.Header.Set("Content-Encoding", "gzip")
		c.Response.Header.Set("Content-Length", strconv.Itoa(buf.Len()))
		c.SetBody(buf.Bytes())
		return
	}

	writeBody := func(w *bufio.Writer) {
		var writer *gzip.Writer
		if useGzip {
			writer = gzip.NewWriter(w)
			defer writer.Close()
		}

		out := w
		if writer != nil {
			out = bufio.NewWriter(writer)
			defer out.Flush()
		}

		if chunked {
			var chunk int64 = 10 * 1024 * 1024
			mod := size / chunk
			remainder := size % chunk
			for i := 0; i < int(mod); i++ {
				fmt.Fprint(out, randStringBytesMaskImprSrc(int(chunk), src))
				if err := out.Flush(); err != nil {
					return
				}
			}

			if remainder > 0 {
				fmt.Fprint(out, randStringBytesMaskImprSrc(int(remainder), src))
			}
		} else {
			fmt.Fprint(out, randStringBytesMaskImprSrc(int(size), src))
		}
	}

	if useGzip {
		c.Response.Header.Set("Content-Encoding", "gzip")
	}

	status := string(c.Request.Header.Peek("X-Status"))
	if status != "" {
		code, err := strconv.Atoi(status)
		if err == nil {
			c.SetStatusCode(code)
		}
	}

	if chunked || useGzip {
		c.SetBodyStreamWriter(writeBody)
	} else {
		fmt.Fprint(c, randStringBytesMaskImprSrc(int(size), src))
	}
}

func fixedDelayResponse(c *fasthttp.RequestCtx, p fasthttprouter.Params) {
	duration, err := time.ParseDuration(p.ByName("delay"))
	if err != nil {
		// handle error
		return
	}

	time.Sleep(duration)
}

func jsonHandler(c *fasthttp.RequestCtx, p fasthttprouter.Params) {

	if err := applyDelay(c, nil); err != nil {
		return
	}

	if err := applySlowdown(c, nil); err != nil {
		return
	}

	switch p.ByName("type") {
	case "invalid":
		fmt.Fprintf(c, `{time": "%s"}`, time.Now().String())
	default:
		fmt.Fprintf(c, `{"time": "%s"}`, time.Now().String())
	}
}

func xmlHandler(c *fasthttp.RequestCtx, _ fasthttprouter.Params) {
	c.SetContentType("application/xml")

	fmt.Fprint(c, xml)
}

func soapHandler(_ *fasthttp.RequestCtx, _ fasthttprouter.Params) {
	// TODO: SOAP Response
}

// https://stackoverflow.com/questions/22892120/how-to-generate-a-random-string-of-a-fixed-length-in-go
func randStringBytesMaskImprSrc(n int, source rand.Source) string {
	b := make([]byte, n)
	// A src.Int63() generates 63 random bits, enough for letterIdxMax characters!
	for i, cache, remain := n-1, source.Int63(), letterIdxMax; i >= 0; {
		if remain == 0 {
			cache, remain = source.Int63(), letterIdxMax
		}
		if idx := int(cache & letterIdxMask); idx < len(letterBytes) {
			b[i] = letterBytes[idx]
			i--
		}
		cache >>= letterIdxBits
		remain--
	}

	return string(b)
}
