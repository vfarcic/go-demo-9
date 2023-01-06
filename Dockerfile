FROM golang:1.19 AS build
ADD . /src
WORKDIR /src
RUN go mod tidy
RUN GOOS=linux GOARCH=amd64 go build -v -o go-demo-9

FROM alpine:latest
RUN mkdir /lib64 && ln -s /lib/libc.musl-x86_64.so.1 /lib64/ld-linux-x86-64.so.2
EXPOSE 8080
CMD ["go-demo-9"]
COPY --from=build /src/go-demo-9 /usr/local/bin/go-demo-9
RUN chmod +x /usr/local/bin/go-demo-9
