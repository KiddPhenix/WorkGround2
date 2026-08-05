const maxSourceBytes = 8 * 1024 * 1024;
const maxAvatarBytes = 64 * 1024;

export async function compressCollaborationAvatar(file: File): Promise<string> {
  if (!file.type.startsWith("image/") || file.size > maxSourceBytes) throw new Error("Avatar must be an image smaller than 8 MB");
  const bitmap = await createImageBitmap(file);
  try {
    const crop = Math.min(bitmap.width, bitmap.height);
    const sx = Math.floor((bitmap.width - crop) / 2);
    const sy = Math.floor((bitmap.height - crop) / 2);
    for (const [size, quality] of [[96, .82], [72, .7], [56, .55]] as const) {
      const canvas = document.createElement("canvas");
      canvas.width = size;
      canvas.height = size;
      const context = canvas.getContext("2d");
      if (!context) throw new Error("Avatar canvas is unavailable");
      context.drawImage(bitmap, sx, sy, crop, crop, 0, 0, size, size);
      const data = canvas.toDataURL("image/webp", quality);
      if (data.length <= maxAvatarBytes) return data;
    }
  } finally {
    bitmap.close();
  }
  throw new Error("Avatar is too complex to compress");
}
