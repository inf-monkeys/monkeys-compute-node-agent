ARG GO_VERSION=1.24
ARG KUBECTL_VERSION=v1.35.6

FROM golang:${GO_VERSION}-bookworm AS agent-build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/monkeys-compute-node-agent ./cmd/monkeys-compute-node-agent

FROM alpine:3.21 AS kubectl-download
ARG TARGETARCH
ARG KUBECTL_VERSION
ARG KUBECTL_SHA256_AMD64=5d11e2ba01ea68ffd053f56e27738e2b4330013ee67f7e46c6da6c585d3c9926
ARG KUBECTL_SHA256_ARM64=c0f97f31c9ddc22d4951d543a1a7125a9af4b31e895ad4aa99899c4ba2a6ff0b
RUN apk add --no-cache ca-certificates wget \
    && case "${TARGETARCH}" in \
      amd64) kubectl_sha256="${KUBECTL_SHA256_AMD64}" ;; \
      arm64) kubectl_sha256="${KUBECTL_SHA256_ARM64}" ;; \
      *) echo "unsupported kubectl architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && wget -qO /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
    && echo "${kubectl_sha256}  /usr/local/bin/kubectl" | sha256sum -c - \
    && chmod 0755 /usr/local/bin/kubectl

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ARG KUBECTL_VERSION
LABEL org.opencontainers.image.title="Monkeys Compute Agent" \
      org.opencontainers.image.description="Monkeys Compute Cluster Agent" \
      org.opencontainers.image.source="https://github.com/inf-monkeys/monkeys-compute-node-agent" \
      org.opencontainers.image.version="${VERSION}" \
      io.monkeys.compute.kubectl.version="${KUBECTL_VERSION}"
COPY --from=kubectl-download /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=kubectl-download /usr/local/bin/kubectl /usr/local/bin/kubectl
COPY --from=agent-build /out/monkeys-compute-node-agent /usr/local/bin/monkeys-compute-node-agent
USER 65532:65532
WORKDIR /tmp
ENTRYPOINT ["/usr/local/bin/monkeys-compute-node-agent"]
CMD ["run", "--mode", "cluster", "--state", "/tmp/agent-state.json", "--workspace", "/tmp/monkeys-compute-agent"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/monkeys-compute-node-agent", "version"]
