package rendersandbox

import (
	"bufio"
	"fmt"
	"io"
	"strconv"

	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/template"
)

// The child half of the sandbox.
//
// `Serve` is what `halite-node render-sandbox` runs. It reads render
// requests on stdin and writes answers on stdout, and it is the only
// thing in this process: no configuration is loaded, no key is read, no
// socket is opened, and the environment it was given holds one variable.
// Everything it knows about the run arrived in the request.
//
// Stdout carries frames and nothing else. Anything this process printed
// would land in the middle of one, so the child logs to stderr, which
// the parent hands to the node's log.

// Serve runs the child protocol until stdin closes.
func Serve(in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)
	c := &childServer{in: r, out: out}

	// The parent reads this before it sends anything, so a mismatched
	// pair is found before a pillar crosses the boundary.
	if err := writeFrame(out, frame{Kind: frameHello, Version: codecVersion}); err != nil {
		return err
	}

	for {
		req, err := readFrame(r)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				// The parent finished with us, which is the ordinary way
				// this ends.
				return nil
			}
			return err
		}
		if req.Kind != frameRender {
			return fmt.Errorf("the render sandbox child was sent a %q frame, which is not a request", req.Kind)
		}
		if err := c.render(req); err != nil {
			return err
		}
	}
}

// childServer holds the pipes and the callback numbering.
type childServer struct {
	in  *bufio.Reader
	out io.Writer
	seq int
}

// render answers one request.
func (c *childServer) render(req frame) error {
	f := restoreFiles(req.Files)
	opts, err := decodeOptions(req.Opts, f)
	if err != nil {
		return c.fail(err)
	}

	// The callbacks are installed here rather than carried in the
	// request, because they are the two things the child cannot do.
	if req.Opts.HasLoader {
		opts.Loader = &proxyLoader{c: c}
	}
	if req.Opts.HasSalt {
		opts.Salt = &proxyDispatcher{c: c}
	}
	opts.OnUndefined = c.reportUndefined
	if opts.TemplateOptions != nil {
		// The template engine takes its own copy of the callback, and a
		// permissive render that reported nothing would leave an
		// operator with no way to find the names to fix.
		opts.TemplateOptions.OnUndefined = c.reportUndefined
	}

	res, renderErr := render.RunStages(req.Body, req.Stages, opts)
	if renderErr != nil {
		return c.fail(renderErr)
	}

	outFiles := newFiles()
	wire := &wireResult{
		Text:     res.Text,
		Pipeline: res.Pipeline,
		Warnings: res.Warnings,
	}
	if res.Value != nil {
		node, err := encodeValue(res.Value, outFiles)
		if err != nil {
			return c.fail(fmt.Errorf("the render produced a value the sandbox cannot carry back: %w", err))
		}
		wire.Value = &node
	}
	return writeFrame(c.out, frame{Kind: frameResult, OK: true, Result: wire, Files: outFiles.names})
}

// fail reports a render that did not work. It is not an error of this
// function: the child stays alive and answers the next request, because
// a tree with one broken SLS in it is ordinary.
func (c *childServer) fail(err error) error {
	return writeFrame(c.out, frame{Kind: frameResult, OK: false, Error: err.Error()})
}

// ask sends a callback frame and waits for its reply.
func (c *childServer) ask(f frame) (frame, error) {
	c.seq++
	f.ID = strconv.Itoa(c.seq)
	if err := writeFrame(c.out, f); err != nil {
		return frame{}, err
	}
	reply, err := readFrame(c.in)
	if err != nil {
		return frame{}, err
	}
	if reply.Kind != frameReply {
		return frame{}, fmt.Errorf("expected a reply to %s and got a %q frame", f.Kind, reply.Kind)
	}
	if reply.ID != f.ID {
		return frame{}, fmt.Errorf("a reply to callback %s arrived tagged %s", f.ID, reply.ID)
	}
	return reply, nil
}

// reportUndefined is the one callback with no answer.
func (c *childServer) reportUndefined(name string, pos template.Pos) {
	f := newFiles()
	// A failure here is swallowed on purpose: this is a diagnostic, and
	// a broken pipe will be reported by the render that follows it.
	_ = writeFrame(c.out, frame{
		Kind:  frameUndefined,
		Name:  name,
		Pos:   posOf(pos, f),
		Files: f.names,
	})
}

// proxyLoader resolves a template through the parent.
type proxyLoader struct{ c *childServer }

func (p *proxyLoader) Load(name string) (string, string, error) {
	reply, err := p.c.ask(frame{Kind: frameLoad, Name: name})
	if err != nil {
		return "", "", err
	}
	switch {
	case reply.NotFound:
		// The same error the in-process loader returns, because
		// `{% include ... ignore missing %}` turns on exactly this.
		return "", "", template.ErrNotFound
	case reply.Error != "":
		return "", "", fmt.Errorf("%s", reply.Error)
	}
	return reply.Text, reply.Display, nil
}

// proxyDispatcher calls an execution module in the parent.
type proxyDispatcher struct{ c *childServer }

func (p *proxyDispatcher) CallModule(name string, args []any, kwargs map[string]any) (any, error) {
	f := newFiles()
	wireArgs, err := encodeArgs(args, f)
	if err != nil {
		return nil, fmt.Errorf("%s: an argument the render sandbox cannot carry: %w", name, err)
	}
	wireKwargs, err := encodeKwargs(kwargs, f)
	if err != nil {
		return nil, fmt.Errorf("%s: a keyword argument the render sandbox cannot carry: %w", name, err)
	}
	reply, err := p.c.ask(frame{
		Kind:   frameCall,
		Name:   name,
		Args:   wireArgs,
		Kwargs: wireKwargs,
		Files:  f.names,
	})
	if err != nil {
		return nil, err
	}
	if !reply.OK {
		msg := reply.Error
		if msg == "" {
			msg = "the call failed and said nothing about why"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	if reply.Value == nil {
		return nil, nil
	}
	return decodeValue(*reply.Value, restoreFiles(reply.Files))
}

func (p *proxyDispatcher) HasModule(name string) bool {
	reply, err := p.c.ask(frame{Kind: frameHas, Name: name})
	if err != nil {
		return false
	}
	return reply.OK
}
