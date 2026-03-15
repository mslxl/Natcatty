/**
 * useImageUpload - Handle image paste/drop with base64 conversion
 *
 * Ported from 1code's use-agents-file-upload.ts
 */
import { useCallback, useState } from 'react';

export interface UploadedImage {
  id: string;
  filename: string;
  dataUrl: string;      // data:image/...;base64,... for preview
  base64Data: string;   // raw base64 for API
  mediaType: string;    // MIME type e.g. "image/png"
}

async function fileToDataUrl(file: File): Promise<{ dataUrl: string; base64: string }> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onloadend = () => {
      const dataUrl = reader.result as string;
      const base64 = dataUrl.split(',')[1] || '';
      resolve({ dataUrl, base64 });
    };
    reader.onerror = reject;
    reader.readAsDataURL(file);
  });
}

export function useImageUpload() {
  const [images, setImages] = useState<UploadedImage[]>([]);

  const addImages = useCallback(async (files: File[]) => {
    const imageFiles = files.filter((f) => f.type.startsWith('image/'));
    if (imageFiles.length === 0) return;

    const newImages: UploadedImage[] = await Promise.all(
      imageFiles.map(async (file) => {
        const id = crypto.randomUUID();
        const filename = file.name || `screenshot-${Date.now()}.png`;
        const mediaType = file.type || 'image/png';
        let dataUrl = '';
        let base64Data = '';
        try {
          const result = await fileToDataUrl(file);
          dataUrl = result.dataUrl;
          base64Data = result.base64;
        } catch (err) {
          console.error('[useImageUpload] Failed to convert:', err);
        }
        return { id, filename, dataUrl, base64Data, mediaType };
      }),
    );

    setImages((prev) => [...prev, ...newImages]);
  }, []);

  const removeImage = useCallback((id: string) => {
    setImages((prev) => prev.filter((i) => i.id !== id));
  }, []);

  const clearImages = useCallback(() => {
    setImages([]);
  }, []);

  return { images, addImages, removeImage, clearImages };
}
