# Production UI image: build the Vite app, then serve dist/ statically
# behind nginx, proxying the API, websocket, and health endpoints to the
# Go server (same contract as the Vite dev proxy in vite.config.ts).
FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
