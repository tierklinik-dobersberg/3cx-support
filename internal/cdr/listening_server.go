package cdr

import (
	"bufio"
	"context"
	"encoding/csv"
	"log/slog"
	"net"
	"sync"
)

type ListeningServer struct {
	wg        sync.WaitGroup
	addr      string
	processor Processor

	l *slog.Logger
}

func NewListeningServer(addr string, p Processor, logger *slog.Logger) *ListeningServer {
	l := &ListeningServer{
		addr:      addr,
		l:         logger,
		processor: p,
	}

	return l
}

func (l *ListeningServer) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", l.addr)
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		if err := listener.Close(); err != nil {
			l.l.Error("failed to close listener", "error", err)
		}
	}()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()

		for {
			conn, err := listener.Accept()
			if err != nil {
				l.l.Error("failed to accept connection", "error", err)
				return
			}

			l.wg.Add(1)
			go l.handleConnection(ctx, conn)
		}
	}()

	return nil
}

func (l *ListeningServer) handleConnection(ctx context.Context, conn net.Conn) {
	defer l.wg.Done()

	reader := bufio.NewReader(conn)
	csvReader := csv.NewReader(reader)

	log := l.l.With("peer", conn.RemoteAddr().String())

	for {
		line, err := csvReader.Read()
		if err != nil {
			log.Error("failed to read record", "error", err)
			return
		}

		l.processor.Process(ctx, line, log)
	}
}
