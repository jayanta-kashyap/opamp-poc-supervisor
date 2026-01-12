# Build stage
FROM golang:1.24 AS build
WORKDIR /src

# Copy entire repo
COPY . .

# Use vendor if present (see step 10); otherwise will fetch modules.
ENV CGO_ENABLED=0
RUN go build -o /out/supervisor ./cmd/supervisor

# Runtime stage (no base image pulls)
FROM scratch
COPY --from=build /out/supervisor /supervisor
ENTRYPOINT ["/supervisor"]