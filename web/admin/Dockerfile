# Builds the admin portal from its own lockfile and serves the static
# output. The server's Compose deployment builds this image; the portal owns
# it, the same way it owns its lockfile and its CI.
#
# The API origin is compiled in at build time on purpose: neither portal has a
# production endpoint in its source, so a deployment supplies its own.

# Tags rather than digests: pinning these belongs with the rest of the supply
# chain work, and a digest invented here would simply fail to pull.
FROM node:24-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
ARG VITE_SESAME_API_URL
ENV VITE_SESAME_API_URL=$VITE_SESAME_API_URL \
    VITE_SESAME_SITE_ORIGIN=$VITE_SESAME_SITE_ORIGIN \
    VITE_SESAME_CAPABILITY_PUBLIC_KEY=$VITE_SESAME_CAPABILITY_PUBLIC_KEY
RUN npm run build

FROM nginx:1.31-alpine
# Both portals are single-page applications: every path that is not a file has
# to return index.html, or a reload of /account or /reset-password is a 404.
# The header set is generated from dist/_headers during the build, so the
# policy the portal ships is the policy nginx serves. The administration
# console additionally must never be indexed or cached.
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
