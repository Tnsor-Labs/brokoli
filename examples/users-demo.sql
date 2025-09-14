CREATE TABLE "geos" (
  "id" INTEGER PRIMARY KEY,
  "lat" TEXT,
  "lng" TEXT
);

CREATE TABLE "addresses" (
  "id" INTEGER PRIMARY KEY,
  "suite" TEXT,
  "zipcode" TEXT,
  "city" TEXT,
  "geo_id" INTEGER,
  "street" TEXT,
  FOREIGN KEY ("geo_id") REFERENCES "geos" ("id") ON DELETE CASCADE
);

CREATE TABLE "companies" (
  "id" INTEGER PRIMARY KEY,
  "bs" TEXT,
  "catchPhrase" TEXT,
  "name" TEXT
);

CREATE TABLE "users" (
  "id" INTEGER PRIMARY KEY,
  "address_id" INTEGER,
  "phone" TEXT,
  "website" TEXT,
  "company_id" INTEGER,
  "name" TEXT,
  "username" TEXT,
  "email" TEXT,
  FOREIGN KEY ("address_id") REFERENCES "addresses" ("id") ON DELETE CASCADE,
  FOREIGN KEY ("company_id") REFERENCES "companies" ("id") ON DELETE CASCADE
);

INSERT INTO "geos" ("id", "lat", "lng") VALUES
(31, '-37.3159', '81.1496'),
(32, '-43.9509', '-34.4618'),
(33, '-68.6102', '-47.0653'),
(34, '29.4572', '-164.2990'),
(35, '-31.8129', '62.5342'),
(36, '-71.4197', '71.7478'),
(37, '24.8918', '21.8984'),
(38, '-14.3990', '-120.7677'),
(39, '24.6463', '-168.8889'),
(40, '-38.2386', '57.2232');


INSERT INTO "addresses" ("id", "suite", "zipcode", "city", "geo_id", "street") VALUES
(11, 'Apt. 556', '92998-3874', 'Gwenborough', 31, 'Kulas Light'),
(12, 'Suite 879', '90566-7771', 'Wisokyburgh', 32, 'Victor Plains'),
(13, 'Suite 847', '59590-4157', 'McKenziehaven', 33, 'Douglas Extension'),
(14, 'Apt. 692', '53919-4257', 'South Elvis', 34, 'Hoeger Mall'),
(15, 'Suite 351', '33263', 'Roscoeview', 35, 'Skiles Walks'),
(16, 'Apt. 950', '23505-1337', 'South Christy', 36, 'Norberto Crossing'),
(17, 'Suite 280', '58804-1099', 'Howemouth', 37, 'Rex Trail'),
(18, 'Suite 729', '45169', 'Aliyaview', 38, 'Ellsworth Summit'),
(19, 'Suite 449', '76495-3109', 'Bartholomebury', 39, 'Dayna Park'),
(20, 'Suite 198', '31428-2261', 'Lebsackbury', 40, 'Kattie Turnpike');


INSERT INTO "companies" ("id", "bs", "catchPhrase", "name") VALUES
(21, 'harness real-time e-markets', 'Multi-layered client-server neural-net', 'Romaguera-Crona'),
(22, 'synergize scalable supply-chains', 'Proactive didactic contingency', 'Deckow-Crist'),
(23, 'e-enable strategic applications', 'Face to face bifurcated interface', 'Romaguera-Jacobson'),
(24, 'transition cutting-edge web services', 'Multi-tiered zero tolerance productivity', 'Robel-Corkery'),
(25, 'revolutionize end-to-end systems', 'User-centric fault-tolerant solution', 'Keebler LLC'),
(26, 'e-enable innovative applications', 'Synchronised bottom-line interface', 'Considine-Lockman'),
(27, 'generate enterprise e-tailers', 'Configurable multimedia task-force', 'Johns Group'),
(28, 'e-enable extensible e-tailers', 'Implemented secondary concept', 'Abernathy Group'),
(29, 'aggregate real-time technologies', 'Switchable contextually-based project', 'Yost and Sons'),
(30, 'target end-to-end models', 'Centralized empowering task-force', 'Hoeger LLC');


INSERT INTO "users" ("id", "address_id", "phone", "website", "company_id", "name", "username", "email") VALUES
(1, 11, '1-770-736-8031 x56442', 'hildegard.org', 21, 'Leanne Graham', 'Bret', 'Sincere@april.biz'),
(2, 12, '010-692-6593 x09125', 'anastasia.net', 22, 'Ervin Howell', 'Antonette', 'Shanna@melissa.tv'),
(3, 13, '1-463-123-4447', 'ramiro.info', 23, 'Clementine Bauch', 'Samantha', 'Nathan@yesenia.net'),
(4, 14, '493-170-9623 x156', 'kale.biz', 24, 'Patricia Lebsack', 'Karianne', 'Julianne.OConner@kory.org'),
(5, 15, '(254)954-1289', 'demarco.info', 25, 'Chelsey Dietrich', 'Kamren', 'Lucio_Hettinger@annie.ca'),
(6, 16, '1-477-935-8478 x6430', 'ola.org', 26, 'Mrs. Dennis Schulist', 'Leopoldo_Corkery', 'Karley_Dach@jasper.info'),
(7, 17, '210.067.6132', 'elvis.io', 27, 'Kurtis Weissnat', 'Elwyn.Skiles', 'Telly.Hoeger@billy.biz'),
(8, 18, '586.493.6943 x140', 'jacynthe.com', 28, 'Nicholas Runolfsdottir V', 'Maxime_Nienow', 'Sherwood@rosamond.me'),
(9, 19, '(775)976-6794 x41206', 'conrad.com', 29, 'Glenna Reichert', 'Delphine', 'Chaim_McDermott@dana.io'),
(10, 20, '024-648-3804', 'ambrose.net', 30, 'Clementina DuBuque', 'Moriah.Stanton', 'Rey.Padberg@karina.biz');


