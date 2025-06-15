# Use official Python runtime as base image
FROM python:3.11-slim

# Set environment variables
ENV PYTHONUNBUFFERED=1
ENV PYTHONDONTWRITEBYTECODE=1
ENV PORT=1080
ENV HOST=0.0.0.0

# Create app directory and non-root user for security
RUN groupadd -r appuser && useradd -r -g appuser appuser
WORKDIR /app

# Copy requirements first for better Docker layer caching
COPY requirements.txt* ./

# Install Python dependencies (if requirements.txt exists)
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; fi

# Copy application code
COPY server.py ./
# COPY *.py ./

# Create logs directory and set permissions
RUN mkdir -p /app/logs && \
    chown -R appuser:appuser /app

# Switch to non-root user
USER appuser

# Expose the port
EXPOSE 1080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://localhost:1080')" || exit 1

# Default command
CMD ["python", "server.py", "1080", "0.0.0.0"]