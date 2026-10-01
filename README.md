# BasisVR Social Service

A comprehensive social service backend for BasisVR, built with Go. This service provides real-time communication, user presence tracking, activity streaming, and federation capabilities for the BasisVR ecosystem.

## Overview

BasisVR Social Service is a full-stack application that enables social networking features for BasisVR. It provides a robust backend API, real-time event streaming, and optional ActivityPub federation support.

### Key Features

- **User Management**: Registration, authentication, and profile management
- **Real-time Communication**: WebSocket-based real-time event streaming with Redis
- **Presence Tracking**: Track and broadcast user presence across the platform
- **Activity Feeds**: Social activity streaming and feed generation
- **Asset Management**: Integration with asset catalogs for content management
- **Federation**: Optional ActivityPub support for federation with other platforms
- **Observability**: Built-in metrics, tracing, and logging capabilities
- **Scalability**: Designed for horizontal scaling with Redis and PostgreSQL

## Technology Stack

### Backend
- **Language**: Go
- **Web Framework**: Standard Go HTTP server
- **Database**: PostgreSQL 16
- **Caching & Pub/Sub**: Redis 7
- **File Storage**: MinIO (S3-compatible)

### Services
- **API Server**: Main REST/WebSocket API
- **Delivery Service**: ActivityPub message delivery and federation
- **Retention Service**: Data cleanup and retention policies

### Observability
- **Metrics**: Prometheus
- **Visualization**: Grafana
- **Tracing**: OpenTelemetry Collector

## Project Structure

The repository is organized as follows:
- **backend/**: Go backend application source code
- **frontend/**: Frontend application
- **deploy/**: Deployment configurations
- **infra/**: Infrastructure configurations (Prometheus, Grafana, OTEL)
- **DOCS/**: Comprehensive documentation
- **docker-compose.yml**: Complete development environment setup

## Quick Start

1. Clone the repository
2. Run `docker-compose up` to start all services
3. Access the API at `http://localhost:8080`

All dependent services (PostgreSQL, Redis, MinIO) start automatically with health checks.

## Key Services

- **Backend API** (Port 8080): Main REST/WebSocket API with JWT authentication
- **PostgreSQL** (Port 5432): Primary data store
- **Redis** (Port 6379): Real-time pub/sub and caching
- **MinIO** (Port 9000/9001): S3-compatible object storage
- **Prometheus** (Port 9090): Metrics collection
- **Grafana** (Port 3000): Metrics visualization
- **OpenTelemetry**:

