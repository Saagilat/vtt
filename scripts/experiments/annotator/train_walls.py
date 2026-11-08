#!/usr/bin/env python3
# Стенд разметчика: обучение маленькой U-Net на синтетике.
#
# Модель учится одному: где на карте стены. Двери и области остаются за кадром.
# Вход — RGB 256x256, выход — логит маски стен. После обучения экспортируется ONNX,
# чтобы стенд и будущий Rust-разметчик считали одну и ту же модель.
#
# Использование:
#   python3 scripts/experiments/annotator/train_walls.py --epochs 6

import argparse
import pathlib
import sys

import numpy as np
import torch
from torch import nn
from torch.utils.data import DataLoader, Dataset

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import synthetic  # noqa: E402


class WallDataset(Dataset):
    def __init__(self, images, masks, train):
        self.images = images
        self.masks = masks
        self.train = train

    def __len__(self):
        return len(self.images)

    def __getitem__(self, index):
        image = self.images[index].astype(np.float32) / 255.0
        mask = self.masks[index, 0].astype(np.float32)
        if self.train:
            if np.random.rand() < 0.5:
                image = image[:, ::-1]
                mask = mask[:, ::-1]
            if np.random.rand() < 0.5:
                image = image[::-1, :]
                mask = mask[::-1, :]
        image = np.ascontiguousarray(image.transpose(2, 0, 1))
        mask = np.ascontiguousarray(mask[None])
        return torch.from_numpy(image), torch.from_numpy(mask)


def block(inputs, outputs):
    return nn.Sequential(
        nn.Conv2d(inputs, outputs, 3, padding=1), nn.BatchNorm2d(outputs), nn.ReLU(inplace=True),
        nn.Conv2d(outputs, outputs, 3, padding=1), nn.BatchNorm2d(outputs), nn.ReLU(inplace=True),
    )


class UNet(nn.Module):
    def __init__(self, base=16):
        super().__init__()
        self.down1 = block(3, base)
        self.down2 = block(base, base * 2)
        self.down3 = block(base * 2, base * 4)
        self.down4 = block(base * 4, base * 8)
        self.pool = nn.MaxPool2d(2)
        self.up4 = nn.ConvTranspose2d(base * 8, base * 8, 2, stride=2)
        self.block4 = block(base * 12, base * 4)
        self.up3 = nn.ConvTranspose2d(base * 4, base * 4, 2, stride=2)
        self.block3 = block(base * 6, base * 2)
        self.up2 = nn.ConvTranspose2d(base * 2, base * 2, 2, stride=2)
        self.block2 = block(base * 3, base)
        self.head = nn.Conv2d(base, 1, 1)

    def forward(self, x):
        first = self.down1(x)
        second = self.down2(self.pool(first))
        third = self.down3(self.pool(second))
        fourth = self.down4(self.pool(third))
        out = self.block4(torch.cat([self.up4(fourth), third], dim=1))
        out = self.block3(torch.cat([self.up3(out), second], dim=1))
        out = self.block2(torch.cat([self.up2(out), first], dim=1))
        return self.head(out)


def dice_loss(logits, target):
    probability = torch.sigmoid(logits)
    intersection = (probability * target).sum(dim=(2, 3))
    union = probability.sum(dim=(2, 3)) + target.sum(dim=(2, 3))
    return 1.0 - ((2.0 * intersection + 1.0) / (union + 1.0)).mean()


def iou(logits, target, threshold=0.5):
    predicted = (torch.sigmoid(logits) > threshold).float()
    intersection = (predicted * target).sum().item()
    union = ((predicted + target) > 0).float().sum().item()
    return intersection / union if union else 1.0


def main():
    parser = argparse.ArgumentParser(description='Стенд: U-Net для стен на синтетике')
    parser.add_argument('--count', type=int, default=240)
    parser.add_argument('--size', type=int, default=256)
    parser.add_argument('--epochs', type=int, default=8)
    parser.add_argument('--batch', type=int, default=8)
    parser.add_argument('--out', default='scripts/experiments/annotator/models')
    args = parser.parse_args()

    images, masks = synthetic.build(args.count, args.size)
    split = int(len(images) * 0.9)
    train = WallDataset(images[:split], masks[:split], True)
    valid = WallDataset(images[split:], masks[split:], False)
    train_loader = DataLoader(train, batch_size=args.batch, shuffle=True)
    valid_loader = DataLoader(valid, batch_size=args.batch)

    model = UNet()
    optimizer = torch.optim.Adam(model.parameters(), lr=1e-3)
    for epoch in range(1, args.epochs + 1):
        model.train()
        total = 0.0
        for image, mask in train_loader:
            optimizer.zero_grad()
            logits = model(image)
            loss = nn.functional.binary_cross_entropy_with_logits(logits, mask) + dice_loss(logits, mask)
            loss.backward()
            optimizer.step()
            total += float(loss.item())
        model.eval()
        values = []
        with torch.no_grad():
            for image, mask in valid_loader:
                values.append(iou(model(image), mask))
        print('эпоха %d/%d: loss %.3f, IoU %.3f'
              % (epoch, args.epochs, total / max(1, len(train_loader)), float(np.mean(values))),
              flush=True)

    out_dir = pathlib.Path(args.out)
    out_dir.mkdir(parents=True, exist_ok=True)
    torch.save(model.state_dict(), out_dir / 'annotator_walls.pt')
    dummy = torch.randn(1, 3, args.size, args.size)
    torch.onnx.export(model.eval(), dummy, str(out_dir / 'annotator_walls.onnx'),
                      input_names=['image'], output_names=['mask'],
                      dynamic_axes={'image': {2: 'height', 3: 'width'},
                                    'mask': {2: 'height', 3: 'width'}},
                      opset_version=17, dynamo=False)
    print('сохранено: ' + str(out_dir))
    return 0


if __name__ == '__main__':
    sys.exit(main())
