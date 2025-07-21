package transports

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	ws "github.com/gorilla/websocket"
	"github.com/zishang520/engine.io-go-parser/packet"
	"github.com/zishang520/engine.io/v2/log"
	"github.com/zishang520/engine.io/v2/types"
)

var ws_log = log.NewLog("engine:ws")

type websocket struct {
	Transport

	socket *types.WebSocketConn
	mu     sync.Mutex
}

// WebSocket transport
func MakeWebSocket() Websocket {
	w := &websocket{Transport: MakeTransport()}

	w.Prototype(w)

	return w
}

func NewWebSocket(ctx *types.HttpContext) Websocket {
	w := MakeWebSocket()

	w.Construct(ctx)

	return w
}

func (w *websocket) Construct(ctx *types.HttpContext) {
	w.Transport.Construct(ctx)

	w.socket = ctx.Websocket

	w.socket.On("error", func(errs ...any) {
		w.OnError("websocket error", errs[0].(error))
	})
	w.socket.Once("close", func(...any) {
		w.OnClose()
	})

	go w.message()

	w.SetWritable(true)
	w.SetPerMessageDeflate(nil)
}

// Transport name
func (w *websocket) Name() string {
	return WEBSOCKET
}

// Advertise upgrade support.
func (w *websocket) HandlesUpgrades() bool {
	return true
}

// Receiving Messages
func (w *websocket) message() {
	for {
		mt, message, err := w.socket.NextReader()
		if err != nil {
			if ws.IsUnexpectedCloseError(err) || errors.Is(err, net.ErrClosed) {
				w.socket.Emit("close")
			} else {
				w.socket.Emit("error", err)
			}
			return
		}

		switch mt {
		case ws.BinaryMessage:
			read := types.NewBytesBuffer(nil)
			if _, err := read.ReadFrom(message); err != nil {
				if errors.Is(err, net.ErrClosed) {
					w.socket.Emit("close")
				} else {
					w.socket.Emit("error", err)
				}
			} else {
				w.onMessage(read)
			}
		case ws.TextMessage:
			read := types.NewStringBuffer(nil)
			if _, err := read.ReadFrom(message); err != nil {
				if errors.Is(err, net.ErrClosed) {
					w.socket.Emit("close")
				} else {
					w.socket.Emit("error", err)
				}
			} else {
				w.onMessage(read)
			}
		case ws.CloseMessage:
			w.socket.Emit("close")
			if c, ok := message.(io.Closer); ok {
				c.Close()
			}
			return
		case ws.PingMessage:
		case ws.PongMessage:
		}
		if c, ok := message.(io.Closer); ok {
			c.Close()
		}
	}
}

func (w *websocket) onMessage(data types.BufferInterface) {
	ws_log.Debug(`websocket received "%s"`, data)
	w.Transport.OnData(data)
}

// Writes a packet payload.
func (w *websocket) Send(packets []*packet.Packet) {
	ws_log.Info("Send() called: setting writable=false, starting async send of %d packets", len(packets))
	w.SetWritable(false)
	go w.send(packets)
}
func (w *websocket) send(packets []*packet.Packet) {
	ws_log.Info("send() started: processing batch of %d packets", len(packets))
	startTime := time.Now()

	var sendSuccess bool = false
	defer func() {
		duration := time.Since(startTime)
		if sendSuccess {
			ws_log.Info("send() completed successfully: %d packets sent in %v, emitting drain/ready", len(packets), duration)
			w.Emit("drain")
			w.SetWritable(true)
			w.Emit("ready")
		} else {
			ws_log.Error("send() failed: after %v, keeping transport not writable", duration)
			// If send failed, keep transport not writable to prevent further sends
			w.OnError("websocket send failed", nil)
		}
	}()

	w.mu.Lock()
	defer w.mu.Unlock()

	for i, packet := range packets {
		ws_log.Info("processing packet %d/%d (type=%s)", i+1, len(packets), packet.Type)

		// always creates a new object since ws modifies it
		compress := false
		if packet.Options != nil {
			compress = packet.Options.Compress

			if w.PerMessageDeflate() == nil && packet.Options.WsPreEncodedFrame != nil {
				ws_log.Debug("packet %d: using pre-encoded frame", i+1)
				mt := ws.BinaryMessage
				if _, ok := packet.Options.WsPreEncodedFrame.(*types.StringBuffer); ok {
					mt = ws.TextMessage
				}
				pm, err := ws.NewPreparedMessage(mt, packet.Options.WsPreEncodedFrame.Bytes())
				if err != nil {
					ws_log.Error(`Send Error at packet %d: "%s"`, i, err.Error())
					if errors.Is(err, net.ErrClosed) {
						w.socket.Emit("close")
					} else {
						w.socket.Emit("error", err)
					}
					return // sendSuccess remains false
				}
				if err := w.socket.WritePreparedMessage(pm); err != nil {
					ws_log.Error(`Send Error at packet %d: "%s"`, i, err.Error())
					if errors.Is(err, net.ErrClosed) {
						w.socket.Emit("close")
					} else {
						w.socket.Emit("error", err)
					}
					return // sendSuccess remains false
				}
				ws_log.Info("packet %d: pre-encoded frame sent successfully", i+1)
				continue
			}
		}

		data, err := w.Parser().EncodePacket(packet, w.SupportsBinary())
		if err != nil {
			ws_log.Error(`Send Error at packet %d: "%s"`, i, err.Error())
			if errors.Is(err, net.ErrClosed) {
				w.socket.Emit("close")
			} else {
				w.socket.Emit("error", err)
			}
			return // sendSuccess remains false
		}

		ws_log.Debug("packet %d: encoded successfully, writing to transport (compress=%t)", i+1, compress)
		if !w.write(data, compress) {
			ws_log.Error(`Write failed at packet %d`, i)
			return // sendSuccess remains false
		}
		ws_log.Info("packet %d: written successfully", i+1)
	}

	ws_log.Info("all %d packets processed successfully", len(packets))
	sendSuccess = true // Only set to true if all packets were sent successfully
}
func (w *websocket) write(data types.BufferInterface, compress bool) bool {
	if w.PerMessageDeflate() != nil {
		if data.Len() < w.PerMessageDeflate().Threshold {
			compress = false
		}
	}
	ws_log.Info(`write() starting: data_len=%d, compress=%t`, data.Len(), compress)

	w.socket.EnableWriteCompression(compress)
	mt := ws.BinaryMessage
	if _, ok := data.(*types.StringBuffer); ok {
		mt = ws.TextMessage
	}
	ws_log.Debug(`write() getting writer: message_type=%d`, mt)

	write, err := w.socket.NextWriter(mt)
	if err != nil {
		ws_log.Error(`write() failed to get writer: %s`, err.Error())
		if errors.Is(err, net.ErrClosed) {
			w.socket.Emit("close")
		} else {
			w.socket.Emit("error", err)
		}
		return false
	}

	// Write data
	ws_log.Debug(`write() copying data to writer`)
	if _, err := io.Copy(write, data); err != nil {
		ws_log.Error(`write() failed during data copy: %s`, err.Error())
		write.Close() // Try to close writer before returning error
		if errors.Is(err, net.ErrClosed) {
			w.socket.Emit("close")
		} else {
			w.socket.Emit("error", err)
		}
		return false
	}

	// Close writer
	ws_log.Debug(`write() closing writer`)
	if err := write.Close(); err != nil {
		ws_log.Error(`write() failed to close writer: %s`, err.Error())
		if errors.Is(err, net.ErrClosed) {
			w.socket.Emit("close")
		} else {
			w.socket.Emit("error", err)
		}
		return false
	}

	ws_log.Info(`write() completed successfully`)
	return true
}

// Closes the transport.
func (w *websocket) DoClose(fn types.Callable) {
	ws_log.Debug(`closing`)
	defer w.socket.Close()
	if fn != nil {
		fn()
	}
}
