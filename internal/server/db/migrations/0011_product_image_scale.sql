-- Size of a product image shown instead of the name, in percent of the automatic size (the
-- letters of the logo as high as the letters of the product font). Brand guidelines may ask
-- for a larger or smaller logo.
ALTER TABLE products ADD COLUMN image_scale INTEGER NOT NULL DEFAULT 100;
