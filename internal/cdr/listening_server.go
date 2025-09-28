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

func (lis *ListeningServer) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", lis.addr)
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		if err := listener.Close(); err != nil {
			lis.l.Error("failed to close listener", "error", err)
		}
	}()

	lis.wg.Add(1)
	go func() {
		defer lis.wg.Done()

		for {
			conn, err := listener.Accept()
			if err != nil {
				lis.l.Error("failed to accept connection", "error", err)
				return
			}

			lis.wg.Add(1)
			go lis.handleConnection(ctx, conn)
		}
	}()

	return nil
}

func (lis *ListeningServer) handleConnection(ctx context.Context, conn net.Conn) {
	defer lis.wg.Done()

	reader := bufio.NewReader(conn)
	csvReader := csv.NewReader(reader)

	log := lis.l.With("peer", conn.RemoteAddr().String())

	for {
		line, err := csvReader.Read()
		if err != nil {
			log.Error("failed to read record", "error", err)
			return
		}

		lis.processor.Process(ctx, line, log)
	}
}
