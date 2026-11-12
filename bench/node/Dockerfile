# Baseline image of the ORIGINAL Node.js Noray, for benchmarks only.
# The upstream Dockerfile no longer builds on current npm/pnpm, so this one
# pins pnpm 9 through corepack. Build with the original repo as context:
#   docker build --load -f bench/node/Dockerfile -t noray-node:bench /path/to/foxssake/noray
# (the original's .dockerignore keeps node_modules and tests out).
FROM node:22-alpine
RUN corepack enable && corepack prepare pnpm@9 --activate
WORKDIR /noray
COPY . .
RUN pnpm install --frozen-lockfile --prod=false
EXPOSE 8809/udp 8890/tcp 8891/tcp
ENV NODE_ENV=production
CMD ["node", "bin/noray.mjs"]
