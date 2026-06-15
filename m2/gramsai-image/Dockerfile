FROM ghcr.io/anomalyco/opencode
COPY opencode /usr/local/bin/opencode
RUN chmod +x /usr/local/bin/opencode
RUN apk add --no-cache \
      nodejs npm \
      git \
      python3 py3-pip \
      ripgrep \
      bash coreutils findutils \
      curl wget \
      jq make \
      zip unzip \
      tor \
      openssh-client sshpass \
      ca-certificates
# LSP language servers (Python, TypeScript/JS, Bash) pre-installed so OpenCode
# finds them in PATH instead of downloading per-container at runtime.
RUN npm install -g --no-fund --no-audit \
      pyright \
      typescript typescript-language-server \
      bash-language-server \
      prettier \
  && npm cache clean --force
# Code formatters: ruff (Python: replaces black+isort+flake8), prettier (JS/TS/JSON/CSS/HTML)
RUN pip install --no-cache-dir --break-system-packages ruff
# --- File-creation toolchain (docs, PDF, images, media, data, extra runtime) ---
# Alpine prebuilt packages for the heavy scientific libs (avoids musl pip builds).
RUN apk add --no-cache \
      ffmpeg \
      imagemagick \
      pandoc \
      go \
      py3-pillow \
      py3-numpy py3-pandas py3-matplotlib \
      pango cairo gdk-pixbuf fontconfig \
      font-dejavu ttf-liberation \
      build-base python3-dev libffi-dev
# Office/PDF generation libs via pip (pure-python or small native deps already met above).
RUN pip install --no-cache-dir --break-system-packages \
      weasyprint \
      python-docx openpyxl python-pptx
# Headless plotting backend so matplotlib renders to files without a display.
ENV MPLBACKEND=Agg

# --- Live browser panel: Chromium (musl-native) + Python bridge ---
# Base is Alpine (musl); Playwright's bundled Chromium is glibc and will NOT run.
# Use Alpine native chromium + chromium-swiftshader (software GL, no GPU).
RUN apk add --no-cache \
      chromium \
      chromium-swiftshader \
      nss freetype harfbuzz ttf-freefont \
      py3-aiohttp
# We talk to Chromium over the DevTools Protocol with pure-python websockets.
# (Playwright is NOT used: it ships glibc-only binaries that don't run on Alpine/musl.)
RUN pip install --no-cache-dir --break-system-packages websockets
ENV BRIDGE_CHROMIUM_PATH=/usr/bin/chromium-browser
ENV BRIDGE_PORT=8088
COPY bridge.py /usr/local/bin/bridge.py
RUN python3 -c "import ast; ast.parse(open('/usr/local/bin/bridge.py').read())" && echo "BRIDGE OK"
EXPOSE 8088

COPY tavily-search.js /usr/local/bin/tavily-search.js
RUN chmod +x /usr/local/bin/tavily-search.js
RUN node -c /usr/local/bin/tavily-search.js && echo "JS OK"
COPY memory-search.js /usr/local/bin/memory-search.js
RUN chmod +x /usr/local/bin/memory-search.js
RUN node -c /usr/local/bin/memory-search.js && echo "MEM JS OK"
COPY workspace-download.js /usr/local/bin/workspace-download.js
RUN chmod +x /usr/local/bin/workspace-download.js
RUN node -c /usr/local/bin/workspace-download.js && echo "DL OK"
# Bake the canonical GramsAI config template (opencode.json + 14 agents).
# The entrypoint seeds this into each new user's empty config dir on first run.
RUN mkdir -p /opt/gramsai-config
COPY gramsai-config.tar.gz /opt/gramsai-config/gramsai-config.tar.gz
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
# Verify LSP servers are on PATH
RUN pyright --version && typescript-language-server --version && bash-language-server --version && ruff --version && prettier --version && echo "TOOLS OK"
# Verify the file-creation toolchain
RUN weasyprint --version && pandoc --version | head -1 && ffmpeg -version | head -1 \
  && python3 -c "import docx, openpyxl, pptx, pandas, numpy, matplotlib, PIL; print('PY LIBS OK')" \
  && echo "FILE TOOLS OK"
# Verify the browser bridge toolchain
RUN chromium-browser --version && python3 -c "import websockets, aiohttp; print('BRIDGE LIBS OK')" \
  && echo "BROWSER TOOLS OK"
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
