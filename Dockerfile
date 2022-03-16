FROM golang:alpine AS builder
WORKDIR /app
ADD . .
RUN go install -v .

FROM alpine
COPY --from=builder /go/bin/quay-ci-app /usr/bin/quay-ci-app
EXPOSE 8080
