# ===== Build Stage =====
FROM golang:1.22.2-alpine AS builder

WORKDIR /app

# 빌드 도구 설치
RUN apk add --no-cache git ca-certificates

# 의존성 캐싱을 위해 go.mod, go.sum 먼저 복사
COPY go.mod go.sum ./
RUN go mod download

# 소스 코드 복사
COPY . .

# 바이너리 빌드 (정적 링크, 최적화)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o chatbot ./api

# ===== Runtime Stage =====
FROM alpine:3.19

# 런타임 필수 패키지
RUN apk add --no-cache ca-certificates tzdata

# 비루트 사용자 생성
RUN adduser -D -g '' appuser

WORKDIR /app

# 빌드된 바이너리 복사
COPY --from=builder /app/chatbot .

# 소유권 변경
RUN chown -R appuser:appuser /app

# 비루트 사용자로 실행
USER appuser

# 포트 노출
EXPOSE 58080

# 환경변수 기본값
ENV PORT=58080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q --spider http://localhost:58080/api/v1/health || exit 1

# 실행
CMD ["./chatbot"]
