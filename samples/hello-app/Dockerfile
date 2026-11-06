FROM node:20-alpine
WORKDIR /app
COPY package.json server.js ./
# Run as the unprivileged "node" user that the image already includes
USER node
ENV PORT=3000
EXPOSE 3000
CMD ["node", "server.js"]
