package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ellanetworks/smsc/internal/cluster/listener"
	"github.com/ellanetworks/smsc/internal/db"
	"github.com/hashicorp/raft"
)

const (
	maxJoinBody     = 64 << 10
	joinRate        = time.Second
	joinBurst       = 5
	rpcReadTimeout  = time.Minute
	rpcWriteTimeout = 5 * time.Minute
)

const (
	codeInvalidJoin   = "invalid_join"
	codeInvalidBackup = "invalid_backup"
)

type peerKey struct{}

func peerOf(r *http.Request) string {
	id, _ := r.Context().Value(peerKey{}).(string)
	return id
}

func (n *Node) serveRPC() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /cluster/v1/propose", n.leaderOnly(n.handlePropose))
	mux.HandleFunc("POST /cluster/v1/report", n.leaderOnly(n.handleReport))
	mux.HandleFunc("POST /cluster/v1/join", n.leaderOnly(n.handleJoin))
	mux.HandleFunc("POST /cluster/v1/remove", n.leaderOnly(n.handleRemove))
	mux.HandleFunc("GET /cluster/v1/health", n.leaderOnly(n.handleHealth))
	mux.HandleFunc("GET /cluster/v1/backup", n.leaderOnly(n.handleBackup))
	mux.HandleFunc("POST /cluster/v1/restore", n.leaderOnly(n.handleRestore))

	errorLog := slog.NewLogLogger(n.cfg.Logger.Handler(), slog.LevelDebug)

	n.rpc = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       rpcReadTimeout,
		WriteTimeout:      rpcWriteTimeout,
		ErrorLog:          errorLog,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, peerKey{}, listener.PeerNodeID(c))
		},
	}

	go func() { _ = n.rpc.Serve(n.ln.Stream(listener.ALPNHTTP, n.cfg.Advertise)) }()

	join := http.NewServeMux()
	join.HandleFunc("POST /cluster/v1/join", n.handleBootstrapJoin(newLimiters(joinRate, joinBurst)))

	joinServer := &http.Server{
		Handler:           join,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      time.Minute,
		ErrorLog:          errorLog,
	}

	go func() { _ = joinServer.Serve(n.ln.Stream(listener.ALPNJoin, n.cfg.Advertise)) }()

	n.background.Go(func() {
		<-n.done

		_ = joinServer.Close()
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRPCError(w http.ResponseWriter, err error) {
	var remote *RemoteError

	switch {
	case errors.Is(err, errRetry), errors.Is(err, raft.ErrRaftShutdown):
		writeJSON(w, http.StatusServiceUnavailable, rpcError{Error: err.Error()})
	case errors.As(err, &remote):
		writeJSON(w, remote.Status, rpcError{Error: remote.Message, Code: remote.Code})
	case errors.Is(err, ErrInvalidJoin):
		writeJSON(w, http.StatusBadRequest, rpcError{Error: err.Error(), Code: codeInvalidJoin})
	case errors.Is(err, ErrInvalidBackup):
		writeJSON(w, http.StatusBadRequest, rpcError{Error: err.Error(), Code: codeInvalidBackup})
	default:
		code := db.CodeOf(err)

		status := http.StatusInternalServerError
		if code != "" {
			status = http.StatusConflict
		}

		writeJSON(w, status, rpcError{Error: err.Error(), Code: code})
	}
}

func (n *Node) leaderOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if leading, _ := n.Leader(); leading {
			h(w, r)
			return
		}

		raftNode, _ := n.current()

		if raftNode.State() == raft.Leader {
			writeJSON(w, http.StatusServiceUnavailable, rpcError{Error: "the leader term is starting"})
			return
		}

		writeJSON(w, http.StatusMisdirectedRequest, rpcError{Error: "not the leader"})
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcError{Error: err.Error(), Code: "invalid"})
		return false
	}

	return true
}

func (n *Node) handlePropose(w http.ResponseWriter, r *http.Request) {
	var req proposeRequest
	if !decodeBody(w, r, maxRPCBody, &req) {
		return
	}

	_, b := n.current()
	if b == nil {
		writeJSON(w, http.StatusServiceUnavailable, rpcError{Error: "raft is restarting"})
		return
	}

	results, err := b.propose(r.Context(), req.Commands)

	switch {
	case errors.Is(err, db.ErrBusy):
		writeJSON(w, http.StatusOK, proposeReply{Busy: true})
	case err != nil:
		writeRPCError(w, classify(err))
	default:
		writeJSON(w, http.StatusOK, proposeReply{Results: results, Index: n.db.AppliedIndex()})
	}
}

func (n *Node) handleReport(w http.ResponseWriter, r *http.Request) {
	var rep report
	if !decodeBody(w, r, 1<<16, &rep) {
		return
	}

	n.health.record(peerOf(r), rep)

	writeJSON(w, http.StatusOK, struct{}{})
}

func (n *Node) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, n.health.snapshot())
}

func (n *Node) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if !decodeBody(w, r, maxJoinBody, &req) {
		return
	}

	reply, err := n.admit(r.Context(), req)
	if err != nil {
		writeRPCError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, reply)
}

func (n *Node) handleBootstrapJoin(limits *limiters) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !limits.allow(r.RemoteAddr) {
			writeJSON(w, http.StatusTooManyRequests, rpcError{Error: "too many join requests"})
			return
		}

		var req joinRequest
		if !decodeBody(w, r, maxJoinBody, &req) {
			return
		}

		if _, err := n.verifyToken(r.Context(), req.Token); err != nil {
			writeRPCError(w, err)
			return
		}

		if leading, _ := n.Leader(); leading {
			reply, err := n.admit(r.Context(), req)
			if err != nil {
				writeRPCError(w, err)
				return
			}

			writeJSON(w, http.StatusOK, reply)

			return
		}

		var reply joinReply
		if err := n.peers.call(r.Context(), "/cluster/v1/join", req, &reply); err != nil {
			writeRPCError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, reply)
	}
}

type removeRequest struct {
	NodeID string `json:"node_id"`
}

func (n *Node) handleRemove(w http.ResponseWriter, r *http.Request) {
	var req removeRequest
	if !decodeBody(w, r, 1<<10, &req) {
		return
	}

	if err := n.removeAsLeader(r.Context(), req.NodeID); err != nil {
		writeRPCError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, struct{}{})
}

func (n *Node) handleBackup(w http.ResponseWriter, r *http.Request) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "application/gzip")

	if err := n.backupLocal(r.Context(), w); err != nil {
		n.cfg.Logger.ErrorContext(r.Context(), "failed to stream a backup to a peer", slog.Any("error", err))
	}
}

func (n *Node) handleRestore(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	if err := n.restoreLocal(r.Context(), io.LimitReader(r.Body, maxRestoreBytes)); err != nil {
		writeRPCError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, struct{}{})
}

const maxLimiters = 4096

type limiters struct {
	mu    sync.Mutex
	every time.Duration
	burst int
	by    map[string]*limiter
}

func newLimiters(every time.Duration, burst int) *limiters {
	return &limiters{every: every, burst: burst, by: map[string]*limiter{}}
}

func (l *limiters) allow(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}

	l.mu.Lock()

	lim, ok := l.by[host]
	if !ok {
		if len(l.by) >= maxLimiters {
			clear(l.by)
		}

		lim = newLimiter(l.every, l.burst)
		l.by[host] = lim
	}
	l.mu.Unlock()

	return lim.allow()
}

type limiter struct {
	mu     sync.Mutex
	every  time.Duration
	burst  int
	tokens float64
	last   time.Time
}

func newLimiter(every time.Duration, burst int) *limiter {
	return &limiter{every: every, burst: burst, tokens: float64(burst)}
}

func (l *limiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	if !l.last.IsZero() {
		l.tokens = min(float64(l.burst), l.tokens+float64(now.Sub(l.last))/float64(l.every))
	}

	l.last = now

	if l.tokens < 1 {
		return false
	}

	l.tokens--

	return true
}
