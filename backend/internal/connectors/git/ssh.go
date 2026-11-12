package git

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors/remotefs"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"golang.org/x/crypto/ssh"
)

type sshSession struct {
	client   *ssh.Client
	session  *ssh.Session
	stdin    io.WriteCloser
	stdout   io.Reader
	refs     *packp.AdvRefs
	uploaded bool
	once     sync.Once
	closeErr error
}

func newSSHSession(access *access) (transport.UploadPackSession, error) {
	config, err := access.sshConfig()
	if err != nil {
		return nil, err
	}
	port := access.endpoint.Port
	if port == 0 {
		port = 22
	}
	address := net.JoinHostPort(strings.Trim(access.endpoint.Host, "[]"), strconv.Itoa(port))
	raw, err := access.credential.Proxy.DialContext(access.ctx, address, 10*time.Second)
	if err != nil {
		return nil, err
	}
	watched := remotefs.Watch(access.ctx, raw)
	if err := watched.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		_ = watched.Close()
		return nil, err
	}
	conn, channels, requests, err := ssh.NewClientConn(watched, address, config)
	if err != nil {
		_ = watched.Close()
		return nil, err
	}
	if err := watched.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	client := ssh.NewClient(conn, channels, requests)
	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	result := &sshSession{client: client, session: session}
	if err := result.start(access.endpoint.Path); err != nil {
		_ = result.Close()
		return nil, err
	}
	return result, nil
}

func (s *sshSession) start(repository string) error {
	var err error
	s.stdin, err = s.session.StdinPipe()
	if err != nil {
		return err
	}
	s.stdout, err = s.session.StdoutPipe()
	if err != nil {
		return err
	}
	s.session.Stderr = io.Discard
	// Only a quoted repository path is passed to the standard read-only command.
	quoted := strings.NewReplacer("'", "'\\''", "!", "'\\!'").Replace(repository)
	return s.session.Start("git-upload-pack '" + quoted + "'")
}
func (s *sshSession) AdvertisedReferences() (*packp.AdvRefs, error) {
	return s.AdvertisedReferencesContext(context.Background())
}
func (s *sshSession) AdvertisedReferencesContext(ctx context.Context) (*packp.AdvRefs, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.refs != nil {
		return s.refs, nil
	}
	refs := packp.NewAdvRefs()
	if err := refs.Decode(s.stdout); err != nil {
		if errors.Is(err, packp.ErrEmptyAdvRefs) {
			return nil, transport.ErrEmptyRemoteRepository
		}
		return nil, err
	}
	if refs.IsEmpty() {
		return nil, transport.ErrEmptyRemoteRepository
	}
	transport.FilterUnsupportedCapabilities(refs.Capabilities)
	s.refs = refs
	return refs, nil
}
func (s *sshSession) UploadPack(ctx context.Context, req *packp.UploadPackRequest) (*packp.UploadPackResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.uploaded = true
	if err := req.UploadRequest.Encode(s.stdin); err != nil {
		return nil, err
	}
	if err := req.UploadHaves.Encode(s.stdin, true); err != nil {
		return nil, err
	}
	if err := pktline.NewEncoder(s.stdin).Encodef("done\n"); err != nil {
		return nil, err
	}
	if err := s.stdin.Close(); err != nil {
		return nil, err
	}
	response := packp.NewUploadPackResponse(req)
	if err := response.Decode(&sshResponse{Reader: s.stdout, session: s}); err != nil {
		return nil, err
	}
	return response, nil
}

type sshResponse struct {
	io.Reader
	session *sshSession
}

func (r *sshResponse) Close() error { return r.session.Close() }
func (s *sshSession) Close() error {
	s.once.Do(func() {
		if s.stdin != nil {
			if !s.uploaded {
				_, _ = s.stdin.Write(pktline.FlushPkt)
			}
			_ = s.stdin.Close()
		}
		_ = s.session.Close()
		s.closeErr = s.client.Close()
		if errors.Is(s.closeErr, net.ErrClosed) {
			s.closeErr = nil
		}
	})
	return s.closeErr
}
