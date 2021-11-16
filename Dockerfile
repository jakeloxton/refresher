FROM golang:1.17-alpine as builder

WORKDIR /app

COPY go.mod ./
COPY go.sum ./
RUN go mod download

COPY *.go ./

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build

FROM alpine
COPY --from=builder /app/refresher /

CMD [ "/refresher" ]
