FROM golang:1.26

ENV GOTOOLCHAIN=local
WORKDIR /app

COPY go.mod go.sum ./
RUN for attempt in 1 2 3 4 5; do go mod download && break; if [ "$attempt" -eq 5 ]; then exit 1; fi; sleep 5; done

COPY . .
RUN go build ./...

CMD ["bash"]
