FROM node:26.8.1-alpine@sha256:2d984a15c9b54fd0aeb608b8e0d0d83529eb34d2966db27a1fb4f1edc3d298a3 AS build
WORKDIR /src
COPY web/account/package.json web/account/package-lock.json ./
RUN npm ci
COPY web/account .
COPY scripts/release-contract.mjs /tools/release-contract.mjs
ARG SESAME_VERSION=0.1.0-dev
ARG SESAME_COMMIT=unknown
ARG VITE_SESAME_API_URL
ARG VITE_SESAME_SITE_ORIGIN
ARG VITE_SESAME_CAPABILITY_PUBLIC_KEY
ENV VITE_SESAME_API_URL=$VITE_SESAME_API_URL \
    VITE_SESAME_SITE_ORIGIN=$VITE_SESAME_SITE_ORIGIN \
    VITE_SESAME_CAPABILITY_PUBLIC_KEY=$VITE_SESAME_CAPABILITY_PUBLIC_KEY
RUN npm run build && node /tools/release-contract.mjs portal account "$SESAME_VERSION" "$SESAME_COMMIT" /src/dist/release.json

FROM nginx:1.31@sha256:05b8cb60c354a44ab824ea6e7dc69b46d50762cdbe728a347a5b656e6fb3d7c4
ARG SESAME_VERSION=0.1.0-dev
ARG SESAME_COMMIT=unknown
ARG SESAME_SOURCE_URL=https://github.com/usesesame/sesame-server
LABEL org.opencontainers.image.title="Sesame account portal" \
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
  '  location = /_headers { return 404; }' \
  '  location = /security-headers.conf { return 404; }' \
  '  location / { try_files $uri $uri/ /index.html; }' \
  '}' > /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist /usr/share/nginx/html
COPY --from=build /src/dist/security-headers.conf /etc/nginx/sesame-security-headers.conf
EXPOSE 8080
