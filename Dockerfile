# syntax=docker/dockerfile:1
# Base images are pinned by digest; Dependabot keeps them up to date.

FROM node:26.11.1-trixie-slim@sha256:193fe51b64e77981119c98c2002c9e32a70e2f006fb4d25068ce0558998917f0 AS frontend
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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087
COPY --from=backend /out/kubeportal /kubeportal
COPY --from=frontend /src/dist/kubeportal/browser /web
ENV KUBEPORTAL_STATIC_DIR=/web
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/kubeportal"]
