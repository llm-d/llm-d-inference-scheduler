#!/usr/bin/env python3
"""
Sample external plugins HTTP server
A Python HTTP web server that provides plugins endpoints for llm-d-inference-scheduler.
"""

import json
import logging
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.parse import urlparse
from typing import Dict, List, Any, Optional
import sys
from datetime import datetime


# configure logger
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)


class RequestHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        """Handle POST requests"""
        try:
            parsed_path = urlparse(self.path)
            path = parsed_path.path
            
            # supported endpoints
            if path not in ['/external/sample/filter']:
                self.send_error(404, "Endpoint not found")
                return
            
            # read request body
            content_length = int(self.headers.get('Content-Length', 0))
            if content_length == 0:
                self.send_error(400, "Empty request body")
                return
                
            body = self.rfile.read(content_length)
            try:
                request_data = json.loads(body.decode('utf-8'))
            except json.JSONDecodeError as e:
                self.send_error(400, f"Invalid JSON: {str(e)}")
                return
            
            # Route to appropriate handler
            if path == '/external/sample/filter':
                if not self._validate_filter_request(request_data):
                    self.send_error(400, "Invalid filter request structure")
                    return
                response_data = self._handle_filter(request_data)
            
            # Send successful response
            self._send_json_response(200, response_data)
            
        except Exception as e:
            logger.error(f"Error processing request: {str(e)}")
            self.send_error(500, f"Internal server error: {str(e)}")
    
    def do_GET(self):
        """Handle GET requests - return method not allowed"""
        self.send_error(405, "Method not allowed. Only POST is supported.")
    
    def _validate_filter_request(self, data: Dict[str, Any]) -> bool:
        """validate the filter request structure"""
        logger.info("validating filter request")
        
        try:
            if not self._validate_required_fields(data, ['sched_context', 'pods'], 'filter request payload'):
                return False

            # Validate sched_context structure
            if not self._validate_required_fields(data['sched_context'], ['request', 'pods'], 'sched_context'):
                return False
            
            # Validate request structure
            if not self._validate_required_fields(data['sched_context']['request'], ['target_model', 'request_id', 'critical', 'prompt', 'headers'], 'sched_context>request'):
                return False
            
            # Validate pods array
            if not isinstance(data['pods'], list):
                logger.error("pods must be an array")
                return False
            
            # TODO check pods structure
            return True
            
        except Exception as e:
            logger.error(f"Validation error: {str(e)}")
            return False
    
    def _validate_required_fields(self, data: Dict[str, Any], 
                           required_fields: List[str], 
                           parent_name: str) -> bool:
        logger.info(f"validate fields for {parent_name}")
        for field in required_fields:
            if field not in data:
                logger.error(f"Missing required field in {parent_name}: {field}")
                return False
        logger.info(f"{parent_name} is valid")
        return True
        
    def _handle_filter(self, request_data: Dict[str, Any]) -> List[Dict[str, str]]:
        """
        Handle filter endpoint - filters pods based on the sample logic (filter out all pods with name ending with '2')
        """
        logger.info("Processing filter request")
        
        candidate_pods = request_data['pods']        
        filtered_pods = []
        
        for pod_data in candidate_pods:
            pod = pod_data['pod']
            
            # Check if pod has capacity
            if not pod['namespaced_name']['name'].endswith('hqrcm'):
                logger.info(f">>><<< pod {pod['namespaced_name']['name']} not filtered")
                filtered_pods.append({
                    'name': pod['namespaced_name']['name'],
                    'namespace': pod['namespaced_name']['namespace']
                })
            else:
                logger.info(f">>><<< pod {pod['namespaced_name']['name']} WAS filtered")
        
        logger.info(f"Filtered {len(candidate_pods)} pods down to {len(filtered_pods)}")
        return filtered_pods
     
    def _send_json_response(self, status_code: int, data: Any):
        """Send JSON response"""
        response_json = json.dumps(data, indent=2)
        
        self.send_response(status_code)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(response_json)))
        self.end_headers()
        self.wfile.write(response_json.encode('utf-8'))
    
    def log_message(self, format_str, *args):
        logger.info(f"{self.address_string()} - {format_str % args}")


def run_server(port: int = 1080, host: str = 'localhost'):
    """Run the HTTP server"""
    server_address = (host, port)
    httpd = HTTPServer(server_address, RequestHandler)
    
    logger.info(f"Starting Sample external plugins HTTP server on {host}:{port}")
    
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        logger.info("Server stopped by user")
        httpd.server_close()


if __name__ == '__main__':
    # Parse command line arguments
    port = 1080
    host = 'localhost'
    
    if len(sys.argv) > 1:
        try:
            port = int(sys.argv[1])
        except ValueError:
            print(f"Invalid port number: {sys.argv[1]}")
            sys.exit(1)
    
    if len(sys.argv) > 2:
        host = sys.argv[2]
    
    run_server(port, host)