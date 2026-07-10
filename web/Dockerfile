# Build context: web/. docker build -f web/Dockerfile web/
FROM node:22-alpine AS builder
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:alpine
# Filter envsubst to only the vars this template uses, so nginx's own
# request-time $variables (like $uri, $host) in the config are never touched.
ENV NGINX_ENVSUBST_FILTER=^(QUERY_SERVICE_URL|QUERY_AUTH_TOKEN)$
ENV QUERY_SERVICE_URL=http://query-service:8080
ENV QUERY_AUTH_TOKEN=""
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
