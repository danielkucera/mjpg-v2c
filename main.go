package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"image"
	"image/jpeg"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"time"
	"math/rand"
)

var device *Device

type Device struct {
	Frame         *Frame
	Url	      string
	FrameConfig   image.Config
	LastFrameTime time.Time
	RxBytes       int
	RxBytesLast   int
	RxFrames      int
	RxFramesLast  int
	ChunksLost    int
	FPS           float32
	BPS           float32
}

type Frame struct {
	Number    int
	Complete  bool
	Damaged   bool
	Data      []byte `json:"-"`
	LastChunk int
	Next      *Frame
}

func (f *Frame) waitComplete(ms int) error {
	for i := 0; i < ms || ms == 0; i++ {
		if f.Complete {
			return nil
		}
		time.Sleep(time.Millisecond)
	}
	return errors.New("Waiting for frame timed out")
}

func readStream(){
	resp, err := http.Get(device.Url)
	if err != nil {
		// handle error
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type");
	if len(contentType) < 1 {
		return
	}

	_, mediaTypeParams, err := mime.ParseMediaType(contentType)
	if err != nil {
		return
	}

	boundary := mediaTypeParams["boundary"][2:]

	mr := multipart.NewReader(resp.Body, boundary)
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return
		}
		if err != nil {
			log.Fatal("np: ", err)
		}
		data, err := ioutil.ReadAll(p)
		if err != nil {
			log.Fatal("io: ", err)
		}
		device.Frame.Next = &Frame{}
		device.Frame.Data = data
		device.Frame.Complete = true
		device.Frame = device.Frame.Next
		device.RxFrames++
		device.RxBytesLast = len(data)
		device.RxBytes += device.RxBytesLast
	}
}

func main() {
	dolog, _ := strconv.ParseBool(os.Getenv("MJPGV2C_LOG"))
	listen_string, listen_ok := os.LookupEnv("MJPGV2C_LISTEN")
	if !listen_ok {
		listen_string = ":8080"
	}

	if dolog {
		t := time.Now()
		logName := fmt.Sprintf("mjpgv2c-%d_%02d_%02d-%02d_%02d_%02d.txt",
			t.Year(), t.Month(), t.Day(),
			t.Hour(), t.Minute(), t.Second())
		logFile, err := os.OpenFile(logName, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)
		if err != nil {
			panic(err)
		}

		mw := io.MultiWriter(os.Stdout, logFile)
		log.SetOutput(mw)
		gin.DefaultWriter = mw
	}

	device = &Device{
		Url: os.Args[1],
		Frame: &Frame{},
	}

	log.Println("Program started as: ", os.Args)

	go statistics()
	go readStream()

	router := gin.Default()

	router.GET("/frame.mjpg", func(c *gin.Context) {
		var ifd time.Duration

		frame := device.Frame
		if frame == nil {
			c.String(404, "No frames received")
			c.Abort()
			return
		}

		fps, err := strconv.Atoi(c.DefaultQuery("fps", "0"))
		if fps > 0 && err == nil {
			log.Printf("Client requested %d FPS", fps)
			ifd = time.Duration(1000/fps) * time.Millisecond
		}

		c.Header("Content-Type", "multipart/x-mixed-replace; boundary=--myboundary")

		stopStream := true
		c.Stream(func(w io.Writer) bool {
			defer func() {
				stopStream = false
			}()

			for true {

				frame.waitComplete(0)

				if !frame.Damaged {
					content := append(frame.Data, []byte("\r\n")...)

					_, err := w.Write(append([]byte("--myboundary\r\nContent-Type: image/jpeg\r\n\r\n"), content...))
					if err != nil {
					}
				}

				if ifd > 0 {
					time.Sleep(time.Duration(rand.Int31n(2000)) * time.Millisecond)
					frame = device.Frame
				} else {
					frame = frame.Next
				}

			}

			return stopStream
		})

	})

	router.GET("/frame.jpeg", func(c *gin.Context) {
		frame := device.Frame
		if frame == nil {
			c.String(404, "No frames received")
			c.Abort()
			return
		}

		frame.waitComplete(1000)

		c.Data(200, "image/jpeg", frame.Data)
	})

	router.GET("/view", func(c *gin.Context) {
		c.Data(200, "text/html", []byte("<img src='frame.mjpg'>"))
	})

	//TODO: proper status page
	router.GET("/status", func(c *gin.Context) {
		c.JSON(200, device)
	})

	//TODO: redesign
	router.GET("/", func(c *gin.Context) {
		html := "<h2>Available streams</h2>\n<ul>\n"
		html += "<li><a href='view/'>view</a>\n"
		html += "</ul>\n"
		html += "<h2>Status</h2>\n"
		status, _ := json.MarshalIndent(device, "", "\t")
		html += "<pre>" + string(status) + "</pre>"
		c.Header("Content-Type", "text/html")
		c.String(200, html)
	})

	router.Run(listen_string)
}

func statistics() {
	for true {
		device.BPS = float32(device.RxBytes - device.RxBytesLast)
		device.FPS = float32(device.RxFrames - device.RxFramesLast)
		device.RxBytesLast = device.RxBytes
		device.RxFramesLast = device.RxFrames
		go func(frame *Frame) {
			frame.waitComplete(1000)
			device.FrameConfig, _ = jpeg.DecodeConfig(bytes.NewReader(frame.Data))
		}(device.Frame)
		time.Sleep(time.Second)
		log.Printf("FPS: %f, kBps: %f", device.FPS, device.BPS/1024)
	}
}

