package glsl

import (
	"fmt"
	"image"
	"os"
	"sync"
	"time"

	"github.com/go-gl/gl/v3.3-core/gl"
	"github.com/go-gl/glfw/v3.1/glfw"
)

const vertexShader = `
	attribute vec3 vert;
	void main(void) {
		gl_Position = vec4(vert, 1.0);
	}
`

var glfwInitOnce sync.Once

type Shader struct {
	w, h uint

	win              *glfw.Window
	fbo, rbo, canvas uint32
	vertLoc          uint32
	pbos             [3]uint32

	uniforms    map[string]Uniform
	program     uint32
	curBufIndex int
}

func NewShader(width, height uint, fragmentShader string) (*Shader, error) {
	var err error
	glfwInitOnce.Do(func() {
		err = glfw.Init()
	})
	if err != nil {
		return nil, err
	}

	glfw.WindowHint(glfw.Visible, 0)
	glfw.WindowHint(glfw.RedBits, 8)
	glfw.WindowHint(glfw.GreenBits, 8)
	glfw.WindowHint(glfw.BlueBits, 8)
	glfw.WindowHint(glfw.AlphaBits, 8)
	glfw.WindowHint(glfw.DoubleBuffer, 0)
	win, err := glfw.CreateWindow(1<<12, 1<<12, "glsl", nil, nil)
	if err != nil {
		return nil, err
	}
	sh := &Shader{win: win, w: width, h: height}
	sh.win.MakeContextCurrent()

	// Initialize OpenGL
	if err := gl.Init(); err != nil {
		return nil, err
	}

	debug := GLDebugOutput(os.Stderr)
	go func() {
		for dm := range debug {
			if dm.Severity == gl.DEBUG_SEVERITY_HIGH {
				fmt.Fprintf(os.Stderr, "OpenGL severe: %s\n%s\n", dm.Message, dm.Stack)
			} else {
				fmt.Fprintf(os.Stderr, "%v\n", dm)
			}
		}
	}()

	// Set up the render target.
	gl.GenFramebuffers(1, &sh.fbo)
	gl.GenRenderbuffers(1, &sh.rbo)
	gl.BindRenderbuffer(gl.RENDERBUFFER, sh.rbo)
	gl.RenderbufferStorage(gl.RENDERBUFFER, gl.RGBA8, int32(sh.w), int32(sh.h))
	gl.BindFramebuffer(gl.FRAMEBUFFER, sh.fbo)
	gl.FramebufferRenderbuffer(gl.DRAW_FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.RENDERBUFFER, sh.rbo)
	gl.PixelStorei(gl.UNPACK_ALIGNMENT, 1)

	gl.GenBuffers(int32(len(sh.pbos)), &sh.pbos[0])
	for _, bufId := range sh.pbos {
		gl.BindBuffer(gl.PIXEL_PACK_BUFFER, bufId)
		gl.BufferStorage(gl.PIXEL_PACK_BUFFER, int(sh.w*sh.h*4), nil, gl.STREAM_READ)
	}
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, 0)

	// Create the canvas.
	vertices := []float32{
		-1.0, -1.0, 0.0,
		1.0, -1.0, 0.0,
		-1.0, 1.0, 0.0,
		1.0, 1.0, 0.0,
	}
	gl.CreateBuffers(1, &sh.canvas)
	gl.BindBuffer(gl.ARRAY_BUFFER, sh.canvas)
	gl.BufferData(gl.ARRAY_BUFFER, len(vertices)*4, gl.Ptr(&vertices[0]), gl.STATIC_DRAW)

	// Set up the shader.
	sh.program, err = linkProgram(map[uint32]string{
		gl.VERTEX_SHADER:   vertexShader,
		gl.FRAGMENT_SHADER: fragmentShader,
	})
	if err != nil {
		return nil, err
	}
	gl.UseProgram(sh.program)
	sh.vertLoc = uint32(gl.GetAttribLocation(sh.program, gl.Str("vert\x00")))
	gl.EnableVertexAttribArray(sh.vertLoc)
	gl.VertexAttribPointer(sh.vertLoc, 3, gl.FLOAT, false, 0, nil)
	sh.uniforms = ListUniforms(sh.program)
	return sh, nil
}

func (sh *Shader) downloadImage(pboIndex int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, int(sh.w), int(sh.h)))
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, sh.pbos[pboIndex])
	gl.GetBufferSubData(gl.PIXEL_PACK_BUFFER, 0, int(sh.w*sh.h*4), gl.Ptr(&img.Pix[0]))
	return img
}

func (sh *Shader) drawImage(pboIndex int, uniformValues map[string]func(int32)) {
	for name, setValue := range uniformValues {
		if u, ok := sh.uniforms[name]; ok {
			setValue(u.Location)
		}
	}
	gl.DrawArrays(gl.TRIANGLE_STRIP, 0, 4)
	// Start the transfer of the image to the PBO.
	gl.BindBuffer(gl.PIXEL_PACK_BUFFER, sh.pbos[pboIndex])
	gl.ReadPixels(0, 0, int32(sh.w), int32(sh.h), gl.RGBA, gl.UNSIGNED_BYTE, nil)
}

func (sh *Shader) Image(uniformValues map[string]func(int32)) image.Image {
	if uniformValues == nil {
		uniformValues = map[string]func(int32){}
	}
	if _, ok := uniformValues["resolution"]; !ok {
		uniformValues["resolution"] = func(loc int32) {
			gl.Uniform2f(loc, float32(sh.w), float32(sh.h))
		}
	}

	sh.drawImage(0, uniformValues)
	return sh.downloadImage(0)
}

func (sh *Shader) Animate(interval time.Duration, stream chan<- image.Image, cancel <-chan struct{}, uniformValues map[string]func(int32)) {
	if uniformValues == nil {
		uniformValues = map[string]func(int32){}
	}

	var t time.Duration
	for frame := uint64(0); ; frame++ {
		if _, ok := uniformValues["resolution"]; !ok {
			uniformValues["resolution"] = func(loc int32) {
				gl.Uniform2f(loc, float32(sh.w), float32(sh.h))
			}
		}
		uniformValues["time"] = func(loc int32) {
			gl.Uniform1f(loc, float32(t)/float32(time.Second))
		}
		t += interval

		sh.drawImage(int(frame%uint64(len(sh.pbos))), uniformValues)
		if frame < uint64(len(sh.pbos)) {
			continue
		}

		img := sh.downloadImage(int((frame - 1) % uint64(len(sh.pbos))))
		select {
		case <-cancel:
			return
		case stream <- img:
		}
	}
}

func (sh *Shader) Close() error {
	gl.DeleteProgram(sh.program)
	gl.DeleteFramebuffers(1, &sh.fbo)
	gl.DeleteRenderbuffers(1, &sh.rbo)
	gl.DeleteBuffers(1, &sh.canvas)
	gl.DeleteBuffers(int32(len(sh.pbos)), &sh.pbos[0])
	sh.win.Destroy()
	return nil
}