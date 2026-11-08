# Товарные изображения mesto

Источник: встроенный **image_gen** через навык imagegen. Создан новый bitmap asset, а не скопированные изображения сторонних магазинов. Финальный файл сохранён в проекте:

`internal/gateway/web/assets/products-atlas.png`

Это один atlas 3×3. CSS `background-size: 300% 300%` выбирает ячейки без изменения исходного изображения. Изображения также используются в hero, карточках, корзине и заказах. Иконки и favicon — собственные SVG, фотографии — сгенерированный PNG. Внешние CDN и шрифтовые запросы не нужны.

Порядок ячеек: headphones / keyboard / monitor; backpack / lamp / sneakers; kettle / watch / camera.

## Prompt

Use case: product-mockup. Asset type: a single seamless 3 by 3 product photography contact sheet / CSS sprite atlas for an independent Russian marketplace website. Create a square high resolution 1536x1536 image subdivided into EXACTLY NINE equal square cells in a mathematically regular 3 columns x 3 rows grid. Each cell has the same very pale neutral warm-gray solid background (#f2f3f1), no visible grid lines or borders, soft realistic studio shadows. One complete centered object per cell, generously contained within the middle 70% of its own cell with ample margin, no overlapping objects or props from another cell. Modern premium realistic catalog photography, clean e-commerce product cutouts, beautiful tactile materials, not drawings, no human people. Top row left to right: ivory/beige over-ear wireless headphones in three-quarter view; compact cream mechanical computer keyboard with small sage-green accent key in three-quarter top view; silver thin-bezel desktop monitor on silver stand, blank subtle pale sage screen. Middle row left to right: forest green canvas city backpack in three-quarter front view; pale sage green mushroom-shaped table lamp; a pair of cream off-white everyday sneakers, complete shoes fully visible. Bottom row left to right: matte ivory electric kettle with elegant curved spout; graphite smart watch with black silicone strap and minimal dark face; light beige instant-print camera. Keep every cell consistent scale and lighting with each object's full silhouette isolated. No words, logos, branding, letters, captions, numbers, watermarks, labels, product names, graphical UI or prices anywhere. This is one exact-aligned sprite atlas asset, not a web screenshot. No decorative background objects.
