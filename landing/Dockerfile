FROM node:24-alpine AS build

WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci

COPY . .

ARG PUBLIC_SITE_URL=https://panda2-landing.fly.dev
ENV PUBLIC_SITE_URL=$PUBLIC_SITE_URL
ENV ASTRO_TELEMETRY_DISABLED=1

RUN npm run check && npm run build

FROM nginx:1.29-alpine

COPY nginx-security-headers.conf /etc/nginx/security-headers.conf
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 8080
