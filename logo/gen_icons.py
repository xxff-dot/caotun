# caotun logo 生成器:隧道环 + 穿行数据包
# 用法: py logo/gen_icons.py  (在仓库根目录执行)
# 产物: logo/caotun-1024.png(应用图标) logo/caotun-mark.png(透明底) logo/favicon-64.png
import math
import os

from PIL import Image, ImageDraw

S = 1024          # 母版尺寸
RADIUS = 224      # 应用图标圆角
C1 = (59, 130, 246)     # #3B82F6
C2 = (29, 78, 216)      # #1D4ED8
GREEN = (74, 222, 128)  # #4ADE80 数据包
WHITE = (255, 255, 255)


def lerp(a, b, t):
    return tuple(int(a[i] + (b[i] - a[i]) * t) for i in range(3))


def rounded_gradient_bg(size):
    """蓝渐变圆角方底"""
    img = Image.new('RGBA', (size, size), (0, 0, 0, 0))
    grad = Image.new('RGBA', (size, size))
    px = grad.load()
    for y in range(size):
        for x in range(size):
            t = (x + y) / (2 * size)
            px[x, y] = lerp(C1, C2, t) + (255,)
    mask = Image.new('L', (size, size), 0)
    dm = ImageDraw.Draw(mask)
    dm.rounded_rectangle([0, 0, size - 1, size - 1], radius=int(RADIUS * size / S), fill=255)
    img.paste(grad, (0, 0), mask)
    return img


def draw_mark(img, size, ring=WHITE):
    """隧道环(缺口朝右) + 绿色数据包在缺口处;ring 颜色由调用方定"""
    s = size / S
    d = ImageDraw.Draw(img)
    cx, cy = int(500 * s), int(512 * s)
    rb = int(235 * s)   # 环半径
    w = int(86 * s)     # 环宽
    bbox = [cx - rb, cy - rb, cx + rb, cy + rb]

    # 隧道环:缺口朝右(画 35°..325°,余下 70° 为缺口)
    d.arc(bbox, start=35, end=325, fill=ring + (255,), width=w)

    # 环两端圆头
    for ang in (35, 325):
        a = math.radians(ang)
        ex, ey = cx + rb * math.cos(a), cy + rb * math.sin(a)
        r = w / 2
        d.ellipse([ex - r, ey - r, ex + r, ey + r], fill=ring + (255,))

    # 运动轨迹线(环心 → 数据包)
    y = cy
    x0, x1 = cx - int(150 * s), cx + int(120 * s)
    d.line([x0, y, x1, y], fill=ring + (255,), width=int(30 * s))
    r2 = int(15 * s)
    d.ellipse([x0 - r2, y - r2, x0 + r2, y + r2], fill=ring + (255,))
    d.ellipse([x1 - r2, y - r2, x1 + r2, y + r2], fill=ring + (255,))

    # 数据包:缺口处的绿色圆点
    px, pr = cx + int(228 * s), int(96 * s)
    d.ellipse([px - pr, cy - pr, px + pr, cy + pr], fill=GREEN + (255,),
              outline=ring + (255,), width=int(18 * s))


def main():
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    out = os.path.join(root, 'logo')
    os.makedirs(out, exist_ok=True)

    # 应用图标(渐变底)
    app = rounded_gradient_bg(S)
    draw_mark(app, S)
    app.save(os.path.join(out, 'caotun-1024.png'))

    # 透明底标志(文档用):中蓝环,浅色/深色主题下都可见
    mark = Image.new('RGBA', (S, S), (0, 0, 0, 0))
    draw_mark(mark, S, ring=C1)
    mark.save(os.path.join(out, 'caotun-mark.png'))

    # 网页 favicon
    fav = app.resize((64, 64), Image.LANCZOS)
    fav.save(os.path.join(out, 'favicon-64.png'))

    print('logo generated ->', out)


if __name__ == '__main__':
    main()
