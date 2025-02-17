FROM golang:1.23 as gobuild

RUN update-ca-certificates

WORKDIR /go/src/app

COPY go.mod .
COPY go.sum .

ENV GO111MODULE=on
RUN go mod download
RUN go mod verify

COPY ./ ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /go/bin/3cx-support .

FROM gcr.io/distroless/static

COPY --from=gobuild /go/bin/3cx-support /go/bin/3cx-support
EXPOSE 8080

ENTRYPOINT ["/go/bin/3cx-support"]
