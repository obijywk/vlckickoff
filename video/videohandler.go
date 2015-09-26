package video

import (
	"github.com/ziutek/glib"
	"github.com/ziutek/gst"
	"io"
	"log"
	"net"
	"net/http"
	"syscall"
)

type VideoHandlerOptions struct {
	Frequency    int
	Pids         string
	Width        int
	Height       int
	VideoBitrate int
}

type VideoHandler struct {
	pipeline    *gst.Pipeline
	videoDecode *gst.Element
	audioParse  *gst.Element
	multiFdSink *gst.Element
	conns       map[int]net.Conn
}

func (vh *VideoHandler) Start() {
	vh.pipeline.SetState(gst.STATE_PLAYING)
}

func (vh *VideoHandler) Stop() {
	vh.pipeline.SetState(gst.STATE_NULL)
}

func (vh *VideoHandler) ServeHTTP(wr http.ResponseWriter, req *http.Request) {
	conn, _, err := wr.(http.Hijacker).Hijack()
	if err != nil {
		log.Println(err)
		return
	}
	file, err := conn.(*net.TCPConn).File()
	if err != nil {
		log.Println(err)
		return
	}
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		log.Println(err)
		return
	}
	const webMHeader = "HTTP/1.1 200 OK\r\nContent-Type: video/webm\r\n\r\n"
	if _, err := io.WriteString(file, webMHeader); err != nil {
		log.Println(err)
		return
	}
	file.Close()
	vh.conns[fd] = conn
	log.Println("VideoHandler connection", fd)
	vh.multiFdSink.Emit("add", fd)
}

func (vh *VideoHandler) padAdded(element *gst.Element, pad *gst.Pad) {
	videoSinkPad := vh.videoDecode.GetCompatiblePad(pad)
	if videoSinkPad != nil {
		element.Link(vh.videoDecode)
		log.Println("VideoHandler added video pad")
		return
	}
	audioSinkPad := vh.audioParse.GetCompatiblePad(pad)
	if audioSinkPad != nil {
		element.Link(vh.audioParse)
		log.Println("VideoHandler added audio pad")
		return
	}
}

func (vh *VideoHandler) removeFd(fd int32) {
	log.Println("VideoHandler disconnection", fd)
	intFd := int(fd)
	vh.conns[intFd].Close()
	syscall.Close(intFd)
	delete(vh.conns, intFd)
}

func NewVideoHandler(options VideoHandlerOptions) *VideoHandler {
	vh := new(VideoHandler)
	vh.conns = make(map[int]net.Conn)

	hdFilter := gst.NewCapsSimple(
		"video/x-raw",
		glib.Params{
			"format": "I420",
			"width":  &gst.IntRange{1, 1920},
			"height": &gst.IntRange{1, 1080},
		},
	)

	lowResFilter := gst.NewCapsSimple(
		"video/x-raw",
		glib.Params{
			"format":    "I420",
			"width":     int32(options.Width),
			"height":    int32(options.Height),
			"framerate": &gst.Fraction{30000, 1001},
		},
	)

	audioFilter := gst.NewCapsSimple(
		"audio/x-raw",
		glib.Params{
			"channels": int32(2),
		},
	)

	src := gst.ElementFactoryMake("dvbsrc", "DVB source")
	src.SetProperty("frequency", options.Frequency)
	if options.Pids != "" {
		src.SetProperty("pids", options.Pids)
	} else {
		src.SetProperty("pids", "8192")
	}

	demux := gst.ElementFactoryMake("tsdemux", "DVB demux")

	vh.videoDecode = gst.ElementFactoryMake("mpeg2dec", "Video decode")

	videoDeinterlace := gst.ElementFactoryMake("deinterlace", "Video deinterlace")
	videoDeinterlace.SetProperty("method", 2)

	videoScale := gst.ElementFactoryMake("videoscale", "Video scale")
	videoScale.SetProperty("method", 1)

	videoRate := gst.ElementFactoryMake("videorate", "Video rate")

	videoEncode := gst.ElementFactoryMake("vp8enc", "Video encode")
	videoEncode.SetProperty("threads", 4)
	videoEncode.SetProperty("deadline", 1)
	videoEncode.SetProperty("cpu-used", 5)
	videoEncode.SetProperty("token-parts", 2)
	videoEncode.SetProperty("target-bitrate", options.VideoBitrate*1000)
	videoEncode.SetProperty("auto-alt-ref", true)
	videoEncode.SetProperty("arnr-maxframes", 7)
	videoEncode.SetProperty("arnr-strength", 5)

	videoQueue := gst.ElementFactoryMake("queue2", "Video queue")

	vh.audioParse = gst.ElementFactoryMake("ac3parse", "Audio parse")
	audioDecode := gst.ElementFactoryMake("a52dec", "Audio decode")
	audioEncode := gst.ElementFactoryMake("vorbisenc", "Audio encode")
	audioQueue := gst.ElementFactoryMake("queue2", "Audio queue")

	mux := gst.ElementFactoryMake("webmmux", "WebM mux")
	mux.SetProperty("streamable", true)

	sinkQueue := gst.ElementFactoryMake("queue2", "Sink queue")

	vh.multiFdSink = gst.ElementFactoryMake("multifdsink", "Multifd sink")
	vh.multiFdSink.SetProperty("qos", true)
	vh.multiFdSink.SetProperty("sync", true)
	vh.multiFdSink.SetProperty("max-lateness", 10000000)
	vh.multiFdSink.SetProperty("recover-policy", 3) // keyframe
	vh.multiFdSink.SetProperty("sync-method", 2)    // latest-keyframe

	vh.pipeline = gst.NewPipeline("VideoHandler")
	vh.pipeline.Add(src, demux,
		vh.videoDecode, videoDeinterlace, videoScale, videoRate, videoEncode,
		videoQueue,
		vh.audioParse, audioDecode, audioEncode, audioQueue,
		mux, sinkQueue, vh.multiFdSink)

	src.Link(demux)

	demux.Connect("pad-added", (*VideoHandler).padAdded, vh)

	vh.videoDecode.Link(videoDeinterlace)
	videoDeinterlace.LinkFiltered(videoScale, hdFilter)
	videoScale.Link(videoRate)
	videoRate.LinkFiltered(videoEncode, lowResFilter)
	videoEncode.Link(videoQueue)
	videoQueue.Link(mux)

	vh.audioParse.Link(audioDecode)
	audioDecode.LinkFiltered(audioEncode, audioFilter)
	audioEncode.Link(audioQueue)
	audioQueue.Link(mux)

	mux.Link(sinkQueue)
	sinkQueue.Link(vh.multiFdSink)

	vh.multiFdSink.ConnectNoi("client-fd-removed", (*VideoHandler).removeFd, vh)

	log.Println("VideoHandler created")
	return vh
}
