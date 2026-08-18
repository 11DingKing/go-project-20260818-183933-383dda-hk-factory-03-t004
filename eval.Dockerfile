FROM golang:1.26

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build ./...

ENV GOTOOLCHAIN=local
WORKDIR /app

CMD ["bash"]
