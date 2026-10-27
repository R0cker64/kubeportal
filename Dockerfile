# syntax=docker/dockerfile:1
# Base images are pinned by digest; Dependabot keeps them up to date.

FROM node:26.12.1-trixie-slim@sha256:36793bcbb160a6a16cede1e62b93f84e26b78a972a8a4c0b879c1c12f22761ec AS frontend
WORKDIR /src
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

FROM golang:1.27.2-trixie@sha256:47667934874913a449b96faa7ef4777fa285d9ec29126ee80ed61c45b7cccf0e AS backend
WORKDIR /src
COPY backend/go.mod backend/go.sum* ./
RUN go mod download && go mod verify
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kubeportal ./cmd/server

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=backend /out/kubeportal /kubeportal
COPY --from=frontend /src/dist/kubeportal/browser /web
ENV KUBEPORTAL_STATIC_DIR=/web
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/kubeportal"]
