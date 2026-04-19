import os
import os
import tempfile
import pytest
from fastapi.testclient import TestClient
from PIL import Image
import numpy as np
from unittest.mock import patch, MagicMock

# Import the FastAPI app
from main import app

client = TestClient(app)


class TestModelsEndpoint:
    """Test cases for /models/ endpoint"""

    @patch('main.os.path.exists')
    @patch('main.os.listdir')
    @patch('main.os.path.isfile')
    def test_list_models_success(self, mock_isfile, mock_listdir, mock_exists):
        """Test successful model listing"""
        mock_exists.return_value = True
        mock_listdir.return_value = ['model1.h5', 'model2.keras', 'not_model.txt']
        # Mock isfile to return True for model files, False for others
        def isfile_side_effect(path):
            return os.path.basename(path) in ['model1.h5', 'model2.keras']
        mock_isfile.side_effect = isfile_side_effect

        response = client.get("/models/")
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        assert "model1.h5" in data["models"]
        assert "model2.keras" in data["models"]
        assert "not_model.txt" not in data["models"]  # Should filter out non-model files

    @patch('main.os.path.exists')
    def test_list_models_directory_not_exists(self, mock_exists):
        """Test when model directory doesn't exist"""
        mock_exists.return_value = False

        response = client.get("/models/")
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "error"
        assert "does not exist" in data["message"]

    @patch('main.os.path.exists')
    @patch('main.os.listdir')
    def test_list_models_empty_directory(self, mock_listdir, mock_exists):
        """Test when model directory is empty"""
        mock_exists.return_value = True
        mock_listdir.return_value = []

        response = client.get("/models/")
        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        assert data["models"] == []


class TestPredictEndpoint:
    """Test cases for /predict/ endpoint"""

    def create_test_image(self, size=(224, 224)):
        """Create a test PIL image"""
        img = Image.new('RGB', size, color=(128, 128, 128))
        return img

    @patch('main.get_model')
    def test_predict_success(self, mock_get_model):
        """Test successful prediction"""
        # Mock the model
        mock_model = MagicMock()
        mock_model.predict.return_value = [[0.85]]  # Mock prediction result
        mock_get_model.return_value = mock_model

        # Create test image
        img = self.create_test_image()
        img_bytes = tempfile.NamedTemporaryFile(suffix='.png')
        img.save(img_bytes.name)

        with open(img_bytes.name, 'rb') as f:
            response = client.post(
                "/predict/",
                files={"file": ("test.png", f, "image/png")},
                data={"model_name": "test_model.h5"}
            )

        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "success"
        assert "probability" in data
        assert "node_type" in data
        assert "worker_id" in data
        assert "execution_time" in data
        assert data["probability"] == 85.0  # 0.85 * 100

    def test_predict_missing_file(self):
        """Test prediction with missing file"""
        response = client.post(
            "/predict/",
            data={"model_name": "test_model.h5"}
        )
        assert response.status_code == 422  # Validation error

    def test_predict_missing_model_name(self):
        """Test prediction with missing model name"""
        img = self.create_test_image()
        img_bytes = tempfile.NamedTemporaryFile(suffix='.png')
        img.save(img_bytes.name)

        with open(img_bytes.name, 'rb') as f:
            response = client.post(
                "/predict/",
                files={"file": ("test.png", f, "image/png")}
            )
        assert response.status_code == 422  # Validation error

    @patch('main.get_model')
    def test_predict_model_not_found(self, mock_get_model):
        """Test prediction when model loading fails"""
        mock_get_model.side_effect = Exception("Model file not found")

        img = self.create_test_image()
        img_bytes = tempfile.NamedTemporaryFile(suffix='.png')
        img.save(img_bytes.name)

        with open(img_bytes.name, 'rb') as f:
            response = client.post(
                "/predict/",
                files={"file": ("test.png", f, "image/png")},
                data={"model_name": "nonexistent.h5"}
            )

        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "error"
        assert "Model file not found" in data["message"]


class TestPreprocessing:
    """Test image preprocessing functions"""

    def test_preprocess_image_from_file(self):
        """Test image preprocessing"""
        from main import preprocess_image_from_file

        # Create test image
        img = Image.new('RGB', (400, 400), color=(255, 0, 0))
        temp_file = tempfile.NamedTemporaryFile(suffix='.png')
        img.save(temp_file.name)
        temp_file.seek(0)

        # Read bytes
        img_bytes = temp_file.read()

        # Preprocess
        result = preprocess_image_from_file(img_bytes, target_size=(224, 224))

        # Check shape
        assert result.shape == (1, 224, 224, 3)  # Batch size 1, height, width, channels
        assert result.dtype == np.float32

        temp_file.close()


if __name__ == "__main__":
    pytest.main([__file__])