FROM node:24.20.0-alpine@sha256:e67514e5d0f6c46656005e1b693b2ec9d52e80b641307de684d4a015ba7a4eaf AS build
WORKDIR /src
COPY web/admin/package.json web/admin/package-lock.json ./
RUN npm ci
COPY web/admin .
COPY scripts/release-contract.mjs /tools/release-contract.mjs
ARG SESAME_VERSION=0.1.0-dev
ARG SESAME_COMMIT=unknown
ARG VITE_SESAME_API_URL
ENV VITE_SESAME_API_URL=$VITE_SESAME_API_URL
RUN npm run build && node /tools/release-contract.mjs portal admin "$SESAME_VERSION" "$SESAME_COMMIT" /src/dist/release.json

FROM nginx:1.31@sha256:b34848eff6db786b6b1282d3a9c3fd0b5563dfb6d261df4923378b419e0d24f0
ARG SESAME_VERSION=0.1.0-dev
ARG SESAME_COMMIT=unknown
ARG SESAME_SOURCE_URL=https://github.com/usesesame/sesame-server
LABEL org.opencontainers.image.title="Sesame administration portal" \
      org.opencontainers.image.version="${SESAME_VERSION}" \
      org.opencontainers.image.revision="${SESAME_COMMIT}" \
      org.opencontainers.image.source="${SESAME_SOURCE_URL}"
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install --only-upgrade --no-install-recommends -y \
      libssl3t64=3.5.7-1~deb13u2 \
      openssl=3.5.7-1~deb13u2 \
      openssl-provider-legacy=3.5.7-1~deb13u2 && \
    rm -rf /var/lib/apt/lists/*
RUN printf '%s\n' \
  'server {' \
  '  listen 8080;' \
  '  server_tokens off;' \
  '  root /usr/share/nginx/html;' \
  '  include /etc/nginx/sesame-security-headers.conf;' \
  '  add_header X-Robots-Tag "noindex, nofollow" always;' \
  '  location = /_headers { return 404; }' \
  '  location = /security-headers.conf { return 404; }' \
  '  location / { try_files $uri $uri/ /index.html; }' \
  '}' > /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist /usr/share/nginx/html
COPY --from=build /src/dist/security-headers.conf /etc/nginx/sesame-security-headers.conf
EXPOSE 8080
